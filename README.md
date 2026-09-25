# photosort (image-review-harness)

Cull and tag large event-photo sets: focus tier first, then subject, composition, keywords,
adjectives and editor-style quality remarks. Runs in the homelab cluster next to Ollama; a web UI
picks folders, shows progress, and lets you review and override results.

```
web/            React + Vite + Tailwind UI (served by nginx, proxies /api and /media)
photosort/      Python package: local stage (YOLO pose + sharpness), backends, job runner, FastAPI
docker/         api.Dockerfile (python + torch cu124), ui.Dockerfile (nginx), nginx.conf
.ci/chart/      Helm chart on mononen-library-chart (components: photosort = ui, api = worker)
.github/        CI: test-py, test-web, build-api, build-ui, deploy (homelab runners + buildkit)
tests/          pytest (no GPU/network)
dev-data/       9 synthetic 20 MP test images
docs/           STATUS.md is at the repo root; docs/CLI.md covers the command-line workflow
```

## How it works

1. **Local stage** (free): YOLO11n-pose finds people and head keypoints; sharpness (contrast-normalized
   Laplacian variance on the original pixels) is scored for head / torso / body / background. Inside the
   head box, OpenCV's YuNet face detector locates the eyes (pose eye keypoints as a fallback), and a band
   across both eyes is scored with the Laplacian and an FFT upper-mid-frequency energy ratio, which falls
   faster for slight defocus. With eyes found, both must clear their thresholds; without eyes (helmet,
   visor, turned away), the head box decides. Produces a local focus tier (0 nobody, 1 partial, 2 sharp),
   a 1568 px frame, a native-res head crop and a thumbnail.
   Camera EXIF (aperture, shutter, focal length, ISO) is read as a static prior: entrance pupil >= 40 mm or
   f-number <= 2 flags "very shallow depth of field", a shutter slower than the 1/focal rule flags motion-blur
   risk. A slow-shutter shot with only *borderline* head sharpness is demoted to tier 1; the prior is also
   handed to the vision model and shown in the UI. `config.exif` holds the knobs (`crop_factor` for APS-C).
2. **Vision model**: Ollama `qwen3-vl:4b-instruct` on the RTX 4000 (default, $0), or Gemini / Anthropic
   batch APIs via the CLI. Returns focus tier + notes, subject, composition, placement, action, keywords,
   adjectives, caption, remarks, 1-5 score, keeper. Local and model tiers that disagree are flagged for review.
3. **Export**: CSV/JSONL, XMP sidecars (keywords, description, rating, `PhotoSort|…` hierarchical keywords),
   and a sorted `focus_N/subject/composition/` tree.

## Run locally

```bash
python3 -m venv .venv && . .venv/bin/activate && pip install -e . pytest
make dev                       # API + worker on :8080 against dev-data (state in ./photosort_work)
make web                       # Vite dev server on :5173 proxying /api
pytest -q tests
```
Set `OLLAMA_HOST=http://…:11434` to tag with a local model server; without it the UI's "run vision model"
option will report that the backend is batch-only.

## Deploy

CI builds `registry.adoah.dev/projects/photosort-{api,ui}` and runs `helm upgrade --install photosort
.ci/chart -n production` on the default branch, like stasharr. Manually: `make push VERSION=x && make deploy VERSION=x`.
Cluster prerequisites (in the homelab repo): `photos-ro-claim` (read-only CephFS `/photos`),
`photosort-data-claim` (RBD, state + cache + exports), and the `ollama` HelmRelease with `qwen3-vl:4b-instruct`.
The chart itself owns `photosort-models` (RBD, `helm.sh/resource-policy: keep`): YOLO weights and the
torch/ultralytics caches live there (`PHOTOSORT_MODELS=/models`), seeded from the image on first start.
The API pod shares the GPU cooperatively (nvidia runtime class + `NVIDIA_VISIBLE_DEVICES=all`, no GPU
resource request) exactly like the stasharr transcoder.

## Throughput (measured)

- Local stage: ~0.6 s/image single-threaded on an M1 Pro; on the GPU node the detector is ~10 ms and the
  rest is CPU decode/encode, so give the pod cores.
- Vision model on the Quadro RTX 4000: ~7 s/image with the prompt cached, ~9-10 s cold, and concurrency does
  not help (grammar-constrained decoding serializes). 20k images ≈ 2 days; use "skip nobody-in-focus" to cut it.
