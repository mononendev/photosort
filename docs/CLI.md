# Command line

`photosort` runs the whole pipeline without the web UI. The UI and the CLI share the same state
directory, so you can mix them: analyze in the UI, export from the CLI, or the other way around.

It is one Go binary. From a checkout (Go 1.27):

```bash
make bin                       # bin/photosort
bin/photosort --help
bin/photosort <command> --help
```

`go run ./cmd/photosort …` works too, and `go install ./cmd/photosort` puts it on your `PATH`.

State (database, frames, crops, thumbnails, batch files, `config.json`) lives in the workdir:
`--workdir DIR`, or `$PHOTOSORT_WORKDIR`, default `./photosort_work`. Pass `--workdir` before the command:
`photosort --workdir /data status`. The database is the SQLite file `<workdir>/photosort.db` unless
`$PHOTOSORT_DB` names another (`sqlite://PATH` or `postgres://…`); see [`db copy`](#db-copy---from-url---to-url).

## The analyzer

The commands that touch pixels (`local`, `web`) need the analyzer, the Python service in
[`analyzer/`](../analyzer) that decodes images, runs the pose model and measures sharpness. If
`$PHOTOSORT_ANALYZER` is set (for example `http://127.0.0.1:8090`), they use the analyzer at that URL.
Otherwise they start one as a child process with `uv run` from `./analyzer` (or `$PHOTOSORT_ANALYZER_DIR`;
a binary in `bin/` also finds the `analyzer/` next to it) and stop it when they exit. That needs
[uv](https://docs.astral.sh/uv/); `make analyzer` sets up its environment ahead of time.

Pose models live in `$PHOTOSORT_MODELS/pose` (the current directory if unset; the Makefile uses `./models`).
Install them with [`models get`](#models-get-name-name-).

The detector runs on ONNX Runtime. Where it runs is the analyzer's choice, not a flag: CUDA when the
analyzer's ONNX Runtime has it (the `onnxruntime-gpu` build), else CPU. `$PHOTOSORT_ORT_PROVIDERS` (a
comma-separated list such as `CoreMLExecutionProvider`) overrides it for the analyzer; CPU is always the
fallback. Other providers round differently, so thresholds calibrated on CPU may move slightly.

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
Stage 1. For each image: decode (RAW uses the embedded preview), detect people with the configured pose
model, find the eyes, score sharpness, read EXIF and AF points, assign a local focus tier, and write the
frame, head crop, and thumbnail. The analyzer does the pixel work; the tier rules run in `photosort`. See
[FOCUS.md](FOCUS.md) for what is measured.

| Flag | |
|---|---|
| `--workers N` | Parallel images (default `workers` in config, 4). Decode-bound, so match your cores |
| `--detector NAME` | Pose model for this run, e.g. `yolo11n-pose` or `rtmo-m` (default: `detect_model` in config). It must be installed |
| `--limit N` | Process at most N images |
| `--retry-errors` | Retry images that failed before |

Each row records the pose model that found its people. Analyzing into separate workdirs with different
`--detector` values and comparing them with `scripts/apidiff/compare_local.py` shows what a model change does
to the stored numbers and tiers.

### `calibrate`
Prints percentiles of the chosen metric for every primary subject and writes
`<workdir>/calibration_sheet.jpg`: head crops ordered softest to sharpest, each labeled with its value and
tier. Find where a miss turns soft, soft turns slightly soft, and slightly soft turns sharp, put those three numbers in
`config.json`, and run `rescore`.

| Flag | |
|---|---|
| `--metric eye\|hf\|head` | Eye-band Laplacian (default), eye-band FFT ratio, or head box |
| `--tiles N` | Crops on the sheet (default 64) |

The web UI's Settings page does the same thing interactively, and can also import your own Lightroom
verdicts to suggest thresholds.

### `rescore`
Re-derives local tiers from the stored measurements with the current thresholds. Fast; no images are
decoded and the analyzer isn't needed. It also backfills EXIF and AF points on rows analyzed before those were
read, and re-picks the primary subject. Metrics added in later versions (eye band, FFT) and a different pose
model need a fresh analysis: process the images again in the UI with "re-analyze already done".

### `estimate`
Cost table for the images still waiting on stage 2, across the supported models, at batch pricing.

### `submit`
Stage 2: sends the frame, the head crop, the local measurements, and an EXIF summary to a vision model.

| Flag | |
|---|---|
| `--backend ollama\|gemini\|anthropic` | Default from config (`ollama`) |
| `--model NAME` | Default from config, else per backend: `qwen3-vl:4b-instruct`, `gemini-3.5-flash-lite`, `claude-opus-5` |
| `--base-url URL` | Ollama server (or `$OLLAMA_HOST`) |
| `--concurrency N` | Parallel requests for Ollama; match `OLLAMA_NUM_PARALLEL` (default 2) |
| `--skip-local-tier0` | Don't spend model time on frames with nobody in focus |
| `--sample N` / `--seed S` | Submit only N random images, for a calibration run |
| `--batch-size N` | Requests per batch job (cloud backends; default `batch_size` in config) |
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
| `--focus-source vlm\|local\|strict` | Which tier to sort by (default `focus_source` in config); `strict` takes the lower of the two |
| `--xmp sidecar\|outdir\|none` | Next to the originals (default), under `OUT/xmp/`, or not at all |
| `--xmp-overwrite` | Replace existing sidecars (they are skipped by default) |

### `status`
The workdir and database kind; counts of tracked, analyzed, in-flight, tagged, and errored images; batch
jobs; the local tier breakdown; and the first few errors.

### `web`
Runs the HTTP API and the job runner, and serves the built UI from `web/dist` (or `$PHOTOSORT_WEB_DIST`) when
there is one. This is what the container runs. It starts the analyzer unless `$PHOTOSORT_ANALYZER` is set; if
the analyzer can't be found it still serves, but the local stage and the full-size viewer won't work. When
`$OLLAMA_HOST` is set it overrides the backend and Ollama URL in `config.json`.

| Flag | |
|---|---|
| `--photos DIR` | Photos root the UI can browse (`$PHOTOSORT_PHOTOS`, default `.`); read-only is fine |
| `--host`, `--port` | Default `0.0.0.0:8080` (`$PORT`) |

### `db copy --from URL --to URL`
Copies images, batches, jobs and job items from one database to another, typically the workdir's SQLite file
into Postgres. URLs are `sqlite://PATH` (or a bare path) and `postgres://…`. The target must be empty; its
schema is created on connect, and row counts are checked after each table. Then point `PHOTOSORT_DB` at the
target.

```bash
photosort db copy --from sqlite:///data/photosort.db --to postgres://photosort:…@db/photosort
```

Stop the server first so nothing writes to the source during the copy. The cache, exports and `config.json`
stay in the workdir either way.

### `models list`
Lists the pose models the analyzer knows (`yolo11{n,s,m,l,x}-pose`, `yolo26{n,s,m,l,x}-pose`,
`rtmo-{s,m,l}`) with their family, input size and license, and marks the installed ones. With
`$PHOTOSORT_ANALYZER` set, it asks that analyzer instead and prints its device and installed models.

### `models get NAME [NAME ...]`
Installs pose models into `$PHOTOSORT_MODELS/pose` and records them in its `manifest.json`. YOLO models are
converted from Ultralytics' `.pt` weights to ONNX (YOLO26 keeps its NMS-free end-to-end head); RTMO models are
downloaded as ONNX from OpenMMLab. It runs the analyzer's `photosort_analyzer.models` module through `uv`; for
YOLO names it adds the `ultralytics` extra, so the first YOLO conversion downloads PyTorch (nothing of it is needed
afterwards), while RTMO needs no PyTorch at all. `make models
NAMES="…"` does the same.

In a container, build the models into the analyzer image instead: its `POSE_MODELS` build arg (default
`yolo11n-pose yolo26s-pose rtmo-s`) converts them in a build-only stage.

## Splitting stages across machines

Stage 1 is decode-bound and benefits from many cores; stage 2 with Ollama wants the GPU. The workdir is
portable, so you can run `local` on one machine and `submit`/`sort` on another. Image paths are stored as
the process saw them, so mount the photos at the same path on both (for example `/photos`), or run
`sort` where the photos are. With Postgres (`PHOTOSORT_DB`), both machines can share the database, but the
cache (frames and crops, which `submit` reads) still lives in the workdir.

To use an Ollama server on another host:

```bash
photosort submit --backend ollama --base-url http://gpu-box:11434 --concurrency 1 --skip-local-tier0
```

vLLM isn't an option on Turing cards (compute capability 7.5) for Qwen3-VL; Ollama and llama.cpp run
GGUF on them fine.
