# Deploying photosort

The reference deployment runs on a homelab k3s cluster. The app repo owns its Helm chart and images; a
separate homelab repo owns cluster-level resources (volumes, Ollama).

## Shape of the deployment

- **`photosort`** (UI): nginx serving the built React app; proxies `/api` and `/media` to the API service.
- **`photosort-api`**: one pod, one replica: the Go server (`photosort web`): HTTP API, job runner, EXIF
  and AF points, tier rules, vision backends, export. Image `photosort-api` (~146 MB: Alpine, the binary,
  exiftool). Listens on `:8080`. Mounts the photos read-only at `/photos`, state at `/data`, and `/models`.
- **`photosort-analyzer`**: the pixel stage (decoding, exposure lift, pose detection on CPU ONNX Runtime,
  focus metrics, frame/crop/thumbnail JPEGs) as its own Deployment under an HPA (1-12 pods, 70% of the CPU
  request). Image `photosort-analyzer` (~750 MB unpacked: Python 3.13, ONNX Runtime, OpenCV, Pillow, and a
  seed of converted pose models). Mounts the photos read-only and the shared models volume; nothing
  else, since the JPEGs it makes go back to the API.
- **Ollama** on the GPU node, reached at `http://ollama.<namespace>.svc.cluster.local:11434`.

### How the local stage scales

The API reaches the analyzers through a headless Service, `photosort-analyzer-pool`
(`PHOTOSORT_ANALYZER=dns+http://photosort-analyzer-pool.<namespace>.svc.cluster.local:8090`), re-resolved every
5 s, so it sees each ready pod rather than one virtual IP. Each pod reports its capacity as `slots` on `/health`
(`ANALYZER_SLOTS`, 3 by default, with `PHOTOSORT_ORT_THREADS=2` per inference to fit a 4-core limit). The job
runner keeps one image in flight per slot across all pods (the config's `workers` is ignored in this mode, up to
`PHOTOSORT_ANALYZER_MAX_INFLIGHT`, 256). That keeps every pod near its CPU limit while a local stage runs, which
is what makes the HPA add pods, and their slots are filled as soon as they are ready. Between jobs, or while a
job waits on the vision model, the pods idle and the HPA scales back after 5 minutes.

The config's `analyzer_slots` (the "per pod" field on the Jobs page) overrides every pod's `ANALYZER_SLOTS` without
a redeploy, and takes effect right away, also in a running job; 0 goes back to the pods' own. Each image in flight
holds a decoded full-resolution frame (roughly 0.3 GB for 45 MP, more while an underexposed frame is lifted), so going
above the chart's 3 per pod needs the analyzer's memory limit raised with it.

An image's measure and finalize go to the same pod (the decoded image stays in its memory between them), and
the pod sends the frame, thumbnail and crop JPEGs back in its answers; the API writes them into `/data/cache`,
so the pods need no shared writable volume. When a pod goes away mid-image (scale-down, eviction), the image is
measured again on another, up to twice. The pods' `preStop` sleep lets them leave DNS before uvicorn stops.

The analyzers read any path they are given, so `PHOTOSORT_PHOTOS_ROOT=/photos` makes them refuse paths outside
the photos, and a NetworkPolicy ([`templates/analyzer-pool.yaml`](../.ci/chart/templates/analyzer-pool.yaml))
can admit only the API pod. Deploying it (`analyzerPool.networkPolicy: true`) needs the deploying service account
to manage `networkpolicies` in the `networking.k8s.io` API group in the namespace; without that permission,
`helm upgrade` fails before it changes anything. It only binds
with a CNI that enforces NetworkPolicy (k3s's built-in controller does).

Throughput is roughly linear in pods until something shared gives out: CephFS reads of the originals, the API
pod's CPU (EXIF and AF parsing, writing ~0.5 MB of JPEGs per image; its limit is 4 cores), or SQLite's single
writer (small rows, so this comes last; Postgres removes it). Raise `maxReplicas` to the cores you want to give it.

## Images

| Image | Dockerfile | Notes |
|---|---|---|
| `photosort-api` | [`docker/api.Dockerfile`](../docker/api.Dockerfile) | Static Go binary; runs as uid 568 |
| `photosort-analyzer` | [`docker/analyzer.Dockerfile`](../docker/analyzer.Dockerfile) | Build args `POSE_MODELS`, `ORT`; runs as uid 568 |
| `photosort-ui` | [`docker/ui.Dockerfile`](../docker/ui.Dockerfile) | nginx, [`docker/nginx.conf`](../docker/nginx.conf) |

The analyzer's `POSE_MODELS` build arg (default `yolo11n-pose yolo26s-pose rtmo-s`) names the pose models
converted to ONNX in a build-only stage that has PyTorch and Ultralytics; neither reaches the final image. The
converted models and the YuNet face model are baked in under `/app/weights` as a seed: the analyzer reads the
models volume's manifest over the seed's, and copies a seeded file onto `/models` the first time it's used.
To make another model available in the cluster, add it to `POSE_MODELS` and rebuild.

`make push` builds all three for `linux/amd64` on the remote buildx builder; CI does the same with
`buildctl`. The CI jobs are `test-go` (`go vet`, `go test -race`), `test-analyzer` (`uv run pytest`),
`test-web` (build and lint), a `build` matrix over `api`, `analyzer` and `ui`, and `deploy` (default branch
only).

## Volumes

- `photos-ro-claim` at `/photos`, read-only, in the API and every analyzer pod (it's ReadOnlyMany CephFS).
- `photosort-data-claim` at `/data`: the SQLite database (unless Postgres is used), the frame/crop/thumbnail
  cache (~0.5 MB/image), `config.json` and its history, and exports. ReadWriteOnce, which is why the API runs
  one replica.
- `photosort-models-fs` at `/models`, created by the chart ([`templates/pvc-models.yaml`](../.ci/chart/templates/pvc-models.yaml),
  kept on uninstall): CephFS (`csi-fs-sc`), ReadWriteMany, mounted by the API and every analyzer pod. It holds
  weights only: the face model and the pose store in `/models/pose` (`manifest.json` plus one `.onnx` per
  model), on top of the seed baked into the analyzer image. A model installed onto it is there for the whole pool
  at once; writes go through a temp file and a rename, so a pod never loads a half-written model. `csi-fs-sc`
  needs the Ceph filesystem `fs` to have a `csi` subvolume group (`ceph fs subvolumegroup create fs csi`).

  It replaced `photosort-models` (Ceph RBD, ReadWriteOnce), which the chart no longer manages but leaves on the
  cluster. To carry its models over once the API has rolled onto the new claim, run a one-off pod that mounts
  both (on the GPU node, where the RBD volume was last attached):
  `kubectl -n production run models-copy --rm -it --restart=Never --image=busybox --overrides='{"spec":{"securityContext":{"runAsUser":568,"runAsGroup":568,"fsGroup":568},"volumes":[{"name":"old","persistentVolumeClaim":{"claimName":"photosort-models"}},{"name":"new","persistentVolumeClaim":{"claimName":"photosort-models-fs"}}],"containers":[{"name":"c","image":"busybox","command":["sh","-c","cp -a /old/. /new/ && ls -R /new"],"volumeMounts":[{"name":"old","mountPath":"/old"},{"name":"new","mountPath":"/new"}]}]}}'`,
  then `kubectl -n production delete pvc photosort-models`. Skipping the copy only costs the models that aren't in
  the image's seed; the face model is copied from the seed on first use.

## Postgres (optional)

State is SQLite in `/data/photosort.db` by default. To use Postgres instead:

1. Create a database and a secret with its URL:
   `kubectl -n production create secret generic photosort-db --from-literal=url=postgres://user:pass@host:5432/photosort`
2. Copy the existing data while the API is stopped (scale it to 0, or run the copy in a one-off pod that
   mounts `photosort-data-claim`):
   `photosort db copy --from sqlite:///data/photosort.db --to postgres://…`
   The target must be empty; the copy creates the schema, checks row counts per table, and moves the id
   sequences past the copied rows.
3. Uncomment the `PHOTOSORT_DB` entry (from the `photosort-db` secret) in the API container's `env` in
   [`values.yaml`](../.ci/chart/values.yaml) and deploy.

`/api/health` then reports `"database": "postgres"`. The cache and exports stay on `/data`, so this doesn't
lift the one-replica limit.

## GPU

The API pod carries `runtimeClassName: nvidia` and node affinity to the GPU node; `NVIDIA_VISIBLE_DEVICES` and
`NVIDIA_DRIVER_CAPABILITIES` are set on the analyzer container, the only one that could use the card. There is
no `nvidia.com/gpu` request: the card is shared cooperatively with Ollama.

The analyzer image ships the CPU build of ONNX Runtime by default, and detection runs on CPU. That is a small
cost next to the vision model. To run it on the GPU, build the analyzer with
`--build-arg ORT=onnxruntime-gpu`; it then picks the CUDA execution provider when the card is visible, and
`/api/health` reports `"device": "cuda"`. `PHOTOSORT_ORT_PROVIDERS` on the analyzer container overrides the
choice.

## Running it on another cluster

The chart builds on [mononen-library-chart](https://mononen.github.io/charts/). To point it somewhere
else, change in [`.ci/chart/values.yaml`](../.ci/chart/values.yaml):

- image repositories (`registry.adoah.dev/projects/…`, three of them) and `imageDefaults.pullSecrets`
- `global.namespace`, `global.domain`, and the ingress host, class, and annotations
- the volume claims (`photos-ro-claim`, `photosort-data-claim`) and `modelsVolume.storageClass`
- the Ollama URL in the API container's environment
- the GPU node affinity, `runtimeClassName: nvidia` and the analyzer's `NVIDIA_*` variables, or drop them to
  run on CPU

In the Makefile, `REGISTRY` and `BUILDER`; in `.github/workflows/ci.yml`, the `REGISTRY` env and the
`runs-on: [homelab]` runner labels. The workflow uses no secrets: the self-hosted runners already have
registry push and cluster access, so a GitHub-hosted runner would need both added. Set the repository variable
`PHOTOSORT_TEST_PG` to a Postgres URL to run the database, job and API tests on Postgres too.

Without Kubernetes, the three images run anywhere: see "Build" in the [README](../README.md#build).

## Reference cluster: one-time setup (homelab repo)
- `cluster/apps/production/photosort/pvc.yaml` → `photosort-data-claim` (100Gi RBD). State, cache
  (~0.5 MB/image), thumbnails and exports live here.
- `photos-ro-claim` already exists in `production` (ROX CephFS of `/photos`, also mounted by Immich).
- `cluster/apps/production/ollama/helm-release.yaml`: chart ≥ 1.83.0, `ollama.models.pull:
  [qwen3-vl:4b-instruct]`, `OLLAMA_NUM_PARALLEL=1`, `OLLAMA_FLASH_ATTENTION=1`, `OLLAMA_KV_CACHE_TYPE=q8_0`.
- The chart creates `photosort-models-fs` itself (see Volumes).

## App
```bash
make push VERSION=$(git describe --tags --always)   # buildx via the homelab remote builder → Harbor
make deploy VERSION=...                             # helm upgrade --install photosort .ci/chart -n production
kubectl -n production rollout status deploy/photosort-api
```
UI: https://photosort.adoah.dev (internal ingress, LAN/tailscale only). API health: `/api/health`.

## Probes

- `photosort-api`: readiness is an HTTP GET of `/api/health` on `:8080`. It answers as soon as the server is
  up, whether or not the analyzer is.
- `photosort-analyzer`: readiness runs a small Python one-liner inside the container that fetches
  `http://127.0.0.1:8090/health`, because the analyzer isn't reachable from outside the pod.

## Troubleshooting

`/api/health` returns, among others:

| Field | Meaning |
|---|---|
| `analyzer` | `true` when an analyzer pod answered the last health check (every 30 s) |
| `device` | Where the analyzer runs detection: `cpu` or `cuda`; `null` while the analyzer isn't answering |
| `analyzer_pods`, `analyzer_slots` | The analyzer pool's size as the API sees it, live (`null` with a single analyzer) |
| `analyzer_pod_slots`, `analyzer_slots_per_pod` | A pod's own `ANALYZER_SLOTS`, and the config's `analyzer_slots` override when set |
| `database` | `sqlite` or `postgres` |
| `models_dir` | Where the pose models and face model are read from |
| `current_job` | The running job's id, if any |

- `"analyzer": false`: no analyzer pod is ready. Check `kubectl -n production get pods -l
  app.kubernetes.io/name=photosort-analyzer`, their logs, and that the headless Service has endpoints
  (`kubectl -n production get endpoints photosort-analyzer-pool`). Until it answers, local-stage images fail with an analyzer error and
  the full-size viewer doesn't load; vision-model tagging of already analyzed images still works.
- The pool doesn't grow during a local stage: `kubectl -n production get hpa photosort-analyzer` should show
  CPU near or over 70%. If it reads `<unknown>`, metrics-server isn't running. If the pods sit well below it,
  the API is the bottleneck (its CPU, or reading the photos); the API logs `analyzer pool` with the pods and
  slots it sees whenever that changes.
- A local-stage error saying a pose model "is not installed": the configured `detect_model` (or a job's
  override) isn't in the image's seed or on the models volume. Pick an installed one on the Settings page,
  or add it to `POSE_MODELS` and rebuild the analyzer.

## Gotchas
- The api Deployment has one replica and an RWO data volume; rollouts overlap briefly on the same node.
  Don't scale it. During the overlap the running job stays with the old pod (it holds a heartbeat lease
  on the job row); on SIGTERM it lets in-flight images finish for up to 20 s and hands the job back, and
  the new pod resumes it with done/total intact. A pod killed without that gets its job requeued once the
  lease is 45 s stale; only the images it had in flight are redone. Keep `terminationGracePeriodSeconds`
  at 30 s or more.
- Exports go to `/data/exports/<name>` on the data volume; copy them out with `kubectl cp` or mount the PVC.
