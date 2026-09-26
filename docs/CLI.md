# Command line

`photosort` runs the whole pipeline without the web UI. The UI and the CLI share the same state
directory, so you can mix them: analyze in the UI, export from the CLI, or the other way around.

```bash
pip install -e .
photosort --help
photosort <command> --help
```

State (SQLite database, frames, crops, thumbnails, batch files, `config.json`) lives in the workdir:
`--workdir DIR`, or `$PHOTOSORT_WORKDIR`, default `./photosort_work`. Pass `--workdir` before the command:
`photosort --workdir /data status`.

## Typical run

```bash
photosort scan ~/Pictures/event                  # 1. register files
photosort local --workers 8                      # 2. detect people, score focus, build frames + crops
photosort calibrate --metric eye                 # 3. check the thresholds against your camera
photosort rescore                                #    ...after editing config.json
photosort estimate                               # 4. what stage 2 would cost
photosort submit --backend gemini --sample 200   # 5. tag a sample first, eyeball it
photosort poll --wait
photosort submit --backend gemini                #    then the rest
photosort poll --wait
photosort sort ~/Pictures/event-sorted           # 6. folder tree + CSV/JSONL + XMP sidecars
```

Every step is resumable. Re-running `local` or `submit` only picks up images that aren't done yet.

## Commands

### `scan DIR [DIR ...]`
Registers image files under the given folders (JPEG, PNG, TIFF, WebP, HEIC and RAW). Existing Lightroom
`.xmp` sidecars are read as they're found.

| Flag | |
|---|---|
| `--skip-raw-dupes` | When a JPEG and a RAW share a name, only track the JPEG |

### `local`
Stage 1. For each image: decode (RAW uses the embedded preview), detect people with YOLO11n-pose, find
the eyes, score sharpness, read EXIF, assign a local focus tier, and write the frame, head crop, and
thumbnail. See [FOCUS.md](FOCUS.md) for what is measured.

| Flag | |
|---|---|
| `--workers N` | Parallel workers (default `workers` in config, 4). Decode-bound, so match your cores |
| `--device cuda\|mps\|cpu` | Detector device; auto-detected by default |
| `--limit N` | Process at most N images |
| `--retry-errors` | Retry images that failed before |

### `calibrate`
Prints percentiles of the chosen metric for every primary subject and writes
`<workdir>/calibration_sheet.jpg`: head crops ordered softest to sharpest, each labeled with its value and
tier. Find where a miss turns partial, partial turns soft, and soft turns sharp, put those three numbers in
`config.json`, and run `rescore`.

| Flag | |
|---|---|
| `--metric eye\|hf\|head` | Eye-band Laplacian (default), eye-band FFT ratio, or head box |
| `--tiles N` | Crops on the sheet (default 64) |

The web UI's Calibrate page does the same thing interactively, and can also import your own Lightroom
verdicts to suggest thresholds.

### `rescore`
Re-derives local tiers from the stored measurements with the current thresholds. Fast; no images are
decoded. It also backfills EXIF on rows analyzed before EXIF was read. Metrics added in later versions
(eye band, FFT) need a fresh analysis: process the images again in the UI with "re-analyze already done".

### `estimate`
Cost table for the images still waiting on stage 2, across the supported models, at batch pricing.

### `submit`
Stage 2: sends the frame, the head crop, the local measurements, and an EXIF summary to a vision model.

| Flag | |
|---|---|
| `--backend ollama\|gemini\|anthropic` | Default from config (`ollama`) |
| `--model NAME` | Default per backend: `qwen3-vl:4b-instruct`, `gemini-3.5-flash-lite`, `claude-opus-5` |
| `--base-url URL` | Ollama server (or `$OLLAMA_HOST`) |
| `--concurrency N` | Parallel requests for Ollama; match `OLLAMA_NUM_PARALLEL` (default 2) |
| `--skip-local-tier0` | Don't spend model time on frames with nobody in focus |
| `--sample N` / `--seed S` | Submit only N random images, for a calibration run |
| `--batch-size N` | Requests per batch job (cloud backends) |
| `--retry-errors` | Resubmit images that failed |
| `--dry-run` | Build the requests without sending them |
| `-y`, `--yes` | Skip the cost confirmation |

Ollama runs synchronously and stores each result as it arrives, so it can be interrupted and resumed.
Gemini and Anthropic go through their batch APIs (50% cheaper, results within 24 h): `submit` uploads and
returns, and `poll` collects. API keys come from `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) and
`ANTHROPIC_API_KEY`.

### `poll`
Checks submitted batch jobs and stores finished results.

| Flag | |
|---|---|
| `--wait` | Keep polling until every batch is done |
| `--interval SEC` | Seconds between checks with `--wait` (default 120) |

### `sort OUT`
Stage 3. Writes into `OUT`:

- `focus_<tier>/<subject>/<composition>/…`: the photos, linked or copied
- `review/local<N>_vlm<M>/…`: frames where the local and model focus tiers disagree (always symlinks)
- `results.csv` and `results.jsonl`: every field, one row per image
- XMP sidecars with keywords, caption, star rating, and `PhotoSort|…` hierarchical keywords

| Flag | |
|---|---|
| `--link symlink\|hardlink\|copy\|move` | How photos get into the tree (default `symlink`) |
| `--focus-source vlm\|local\|strict` | Which tier to sort by; `strict` takes the lower of the two |
| `--xmp sidecar\|outdir\|none` | Next to the originals (default), under `OUT/xmp/`, or not at all |
| `--xmp-overwrite` | Replace existing sidecars (they are skipped by default) |

### `status`
Counts of tracked, analyzed, in-flight, tagged, and errored images; batch jobs; the local tier breakdown;
and the first few errors.

### `web`
Runs the FastAPI server and the job worker. This is what the container runs.

| Flag | |
|---|---|
| `--photos DIR` | Photos root the UI can browse (`$PHOTOSORT_PHOTOS`, default `.`); read-only is fine |
| `--host`, `--port` | Default `0.0.0.0:8080` (`$PORT`) |
| `--device` | As for `local` |

## Splitting stages across machines

Stage 1 is decode-bound and benefits from many cores; stage 2 with Ollama wants the GPU. The workdir is
portable, so you can run `local` on one machine and `submit`/`sort` on another. Image paths are stored as
the process saw them, so mount the photos at the same path on both (for example `/photos`), or run
`sort` where the photos are.

To use an Ollama server on another host:

```bash
photosort submit --backend ollama --base-url http://gpu-box:11434 --concurrency 1 --skip-local-tier0
```

vLLM isn't an option on Turing cards (compute capability 7.5) for Qwen3-VL; Ollama and llama.cpp run
GGUF on them fine.
