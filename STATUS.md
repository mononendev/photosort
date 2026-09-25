# Image Review Harness — Status

> 2026-09-25 (later): pivoted to running everything in-cluster with a web UI. See "In-cluster app" below; the CLI sections still apply for cloud-batch runs.

Goal: cull and tag ~20,000 event photos (20 MP, fast cameras, lenses near wide open)
into three focus tiers, plus subject, composition, keywords, adjectives, and
editor-style quality remarks. Focus is the top priority.

## Architecture (decided 2026-09-25)

Two stages. Stage 1 is free and provider-independent; stage 2 is a pluggable
cloud (or local) vision model run through a batch API.

1. **Local stage** (`photosort local`): YOLO11 pose model finds people and their
   head keypoints (works under helmets). Sharpness (contrast-normalized Laplacian
   variance, measured at native resolution) is scored separately for head, torso,
   and full body, and compared against the background. Produces a local focus tier,
   the downscaled frame, and a native-resolution crop centered on the primary
   subject's head/upper body. Head-first scoring is deliberate: at f/1.4-f/2 focus
   varies across a body and the head is what matters.
2. **Cloud stage** (`photosort submit` / `poll`): the model receives the frame, the
   head crop, and the local sharpness numbers, and returns structured JSON: focus
   tier, focus notes, subject, people count, composition, placement, action,
   keywords, adjectives, one-line description, quality remarks, 1-5 score, keeper.
3. **Sort/export** (`photosort sort`): link tree `focus_N/<subject>/<composition>/`,
   JSONL + CSV, XMP sidecars (keywords, description, rating, hierarchical keywords),
   and a `review/` folder where the local and cloud focus tiers disagree.

## What gets uploaded

Frame: 1568 px long edge, JPEG q82 (~300 KB). Crop: 768 px, JPEG q88 (~120 KB).
Every provider caps image tokens near this size, so uploading more resolution buys
nothing. 20k images ≈ 8-9 GB total upload.

## Provider comparison (batch pricing, 50% off; frame + crop + ~350 output tokens)

| Model | 20k images |
|---|---|
| Gemini 2.5 Flash-Lite | ~$4 |
| GPT-5 nano | ~$3 |
| Gemini 3.1 Flash-Lite | ~$10 |
| Gemini 3.5 Flash-Lite (default) | ~$14 |
| Gemini 2.5 Flash | ~$14 |
| GPT-5 mini | ~$14 |
| Gemini 3.8 Flash (promo price) | ~$30 |
| Claude Haiku 4.5 | ~$45 |
| Gemini 3.5 Flash | ~$60 |
| Claude Sonnet 5 | ~$90 |
| Claude Opus 5 | ~$220 |

Google Cloud Vision API (labels/faces) is the wrong tool: no focus or composition
judgment, ~$105 for 20k. Subscriptions (Claude Max, ChatGPT Plus, Google AI Pro)
grant no API credits. Gemini free tier ≈ 500 req/day on Flash-Lite: too slow.

**Decision (revised 2026-09-25 after learning the GPU is a Quadro RTX 4000, 8 GB):**
prefer the **local Ollama backend** (`qwen3-vl:4b-instruct` on k8s-5, $0). Gemini
(`gemini-3.5-flash-lite`) and Anthropic backends remain as paid options, and as a
cheap second opinion for the `review/` disagreements only.

## Compute options for stage 1

- Laptop (M1 Pro): decode-bound, see measured numbers below.
- k8s + Quadro RTX 4000 (8 GB, Turing): detection becomes negligible; throughput
  bound by CPU JPEG decode on the node. `deploy/` has a Dockerfile + Job manifest.

## Local vision model (no API cost) — what was found

- vLLM is out: it dropped Turing (compute capability 7.5) for Qwen3-VL. Ollama and
  llama.cpp run GGUF on the card fine. `deploy/ollama.yaml` is deployed in namespace
  `photosort` (Ceph RBD PVC for models, nvidia runtime class, pinned to the GPU).
- Model: `qwen3-vl:4b-instruct` (Q4_K_M, 3.3 GB). The bare `qwen3-vl:4b` tag is the
  *thinking* variant and ignores `think:false` when a JSON schema is set: it burns the
  whole output budget reasoning and returns empty content. `qwen3-vl:8b` (~6 GB) fits
  only with concurrency 1.
- Greedy decoding (temperature 0) makes the 4B model loop in free-text fields ("No
  consent. No consent. ..."). Fix that works: Qwen's recommended sampling
  (temp 0.7, top_p 0.8, top_k 20, repeat_penalty 1.1) **plus** `maxLength` caps on string
  fields in the schema, which the grammar enforces mechanically. Caps cost decode speed
  (~14 vs ~39 tok/s) but removed all failures: 9/9 parsed, focus tier agreed with the
  local stage on all 9 test images.
- Quality: focus calls correct on the synthetic set; keywords/adjectives usable;
  remarks are shallower than a frontier model and the 4B model mislabels scenes it
  doesn't know (a football coach became "crowd_spectators"). Adequate for tagging;
  focus is carried by the local stage anyway.
- Throughput: see the sweep below. Use `--skip-local-tier0` to not spend GPU time on
  frames the local stage already scored as nothing-in-focus.

## Progress

- [x] Provider research and pricing
- [x] Package skeleton: config, SQLite state, image loading (JPEG/HEIC/RAW)
- [x] Local stage (pose detection, head/torso/body sharpness, crops, cache) — verified on a 9-image synthetic 20 MP set
- [x] Schema + prompt
- [x] Gemini batch backend — request JSONL verified offline; **not yet run against the API (no key set)**
- [x] Anthropic batch backend — request params verified offline; not yet run against the API
- [x] Sort/export + XMP sidecars (well-formed XML verified)
- [x] Calibration tooling (`calibrate`, `rescore`), cost estimate, README
- [x] deploy/ Dockerfile + k8s Job
- [x] Measured throughput on test set (below)
- [ ] First real run: `submit --sample 200` on a real shoot, eyeball results, tune thresholds/prompt
- [ ] OpenAI backend (only if wanted)
- [ ] Optional: local VLM backend for the GPU node (no API cost)

## Measured (M1 Pro, 20 MP JPEGs, 2026-09-25)

| Step | Time |
|---|---|
| JPEG decode | 0.10 s |
| YOLO11n-pose @1280 on MPS | 0.05 s (0.6 s first call) |
| Grayscale + sharpness | 0.06 s |
| Frame JPEG encode | 0.10 s |
| Whole image, single thread | 0.57 s |
| 4 threads, 9 images incl. warmup | 1.7 img/s |

Extrapolation: ~3-4 img/s steady state on the laptop → 20k images in ~1.5-2 h.
On an 8-core node with the RTX 4000 the detector drops to ~10 ms and the rest is CPU
decode/encode → ~15-25 img/s → 20k in ~15-25 min. Confidence: high on the laptop
number (measured), moderate on the node (extrapolated; decode-bound, so cores matter
more than the GPU).

## Open questions

- Which RTX 4000 (Quadro 8 GB vs Ada 20 GB)?
- RAW or JPEG source files? (both supported; RAW uses the embedded preview)


## In-cluster app (2026-09-25)

Repo restructured like stasharr: `web/` (React 19 + Vite + Tailwind 4 + TanStack Query + zustand),
`photosort/` (Python package with FastAPI in `photosort/web/app.py` and a job runner in
`photosort/pipeline.py`), `docker/{api,ui}.Dockerfile`, `.ci/chart` on mononen-library-chart 1.2.0,
`.github/workflows/ci.yml` (homelab runners → buildkit → Harbor → helm), `Makefile`, `tests/`.

- Namespace `production`. Components: `photosort` (nginx UI, ingress photosort.adoah.dev) and
  `photosort-api` (FastAPI + worker; nvidia runtime class + `NVIDIA_VISIBLE_DEVICES=all`, no GPU
  reservation, same pattern as the stasharr transcoder). Mounts `photos-ro-claim` at `/photos`
  (read-only) and `photosort-data-claim` at `/data` (state, cache, thumbs, exports).
- Ollama: the production HelmRelease was updated by the user to chart 1.83.0 / Ollama 0.34.2 with
  `qwen3-vl:4b-instruct`; API uses `http://ollama.production.svc.cluster.local:11434`.
- UI: Dashboard (counts + active job), Browse (folder tree with per-folder tagged/tracked progress,
  multi-select files/folders → Process with options), Photos (filterable grid, detail modal with
  frame + head crop + model output + your overrides), Jobs (progress/cancel), Calibrate (threshold
  contact sheet + re-score), Export (CSV/JSONL/XMP/tree into /data/exports).
- Verified locally end-to-end against dev-data (API smoke test + browser screenshots). Unit tests: 5.
- Homelab repo changes (uncommitted, in ~/Programming/k3s/homelab): `cluster/apps/production/photosort/`
  (PVC + kustomization) and the `photosort` entry in `cluster/apps/production/kustomization.yaml`.
  The PVC was also applied directly.
- Images pushed as `registry.adoah.dev/projects/photosort-{api,ui}:dev` from the laptop via the
  `homelab-remote-builder` buildx endpoint.

### Deploy notes (2026-09-25)
- First `helm upgrade --install` done by hand with `:dev` tags; CI takes over on push.
- The API image is ~4-5 GB (torch cu124 bundles CUDA). k8s-5 has only 48 GB ephemeral storage and hit
  DiskPressure during the first pull, evicting pods; it cleared after image GC. If it recurs, shrink the
  image (torch CPU build + CPU YOLO at ~0.3 s/img on 8 cores is the fallback) or add disk to k8s-5.
- Dockerfile pitfall: ultralytics pulls `opencv-python`, which clashes with `opencv-python-headless`
  (shared `cv2/`); the image removes both and reinstalls headless.

### Next
- First real job through the UI (200 RAW files, Float Life Fest 2024/September/12); watch `/api/health` for `device: cuda`.
- Calibrate focus thresholds on real 20 MP frames (defaults are placeholders).
- Push this repo to GitHub so CI takes over builds/deploys (the workflow expects the same secrets as stasharr).
