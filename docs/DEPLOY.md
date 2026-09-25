# Deploying photosort

The reference deployment runs on a homelab k3s cluster. The app repo owns its Helm chart and images; a
separate homelab repo owns cluster-level resources (volumes, Ollama).

## Shape of the deployment

- **`photosort`** (UI): nginx serving the built React app; proxies `/api` and `/media` to the API service.
- **`photosort-api`**: FastAPI plus the job worker, one replica. Mounts the photos read-only at `/photos`,
  state at `/data`, and model weights at `/models`.
- **Ollama** on the same GPU node, reached at `http://ollama.<namespace>.svc.cluster.local:11434`.

## Running it on another cluster

The chart builds on [mononen-library-chart](https://mononen.github.io/charts/). To point it somewhere
else, change in [`.ci/chart/values.yaml`](../.ci/chart/values.yaml):

- image repositories (`registry.adoah.dev/projects/…`) and `imageDefaults.pullSecrets`
- `global.namespace`, `global.domain`, and the ingress host, class, and annotations
- the volume claims (`photos-ro-claim`, `photosort-data-claim`) and `modelsVolume.storageClass`
- the Ollama URL in the API's environment
- the GPU node affinity and `runtimeClassName: nvidia`, or drop them to run on CPU

In the Makefile, `REGISTRY` and `BUILDER`; in `.github/workflows/ci.yml`, the `REGISTRY` env and the
`runs-on: [homelab]` runner labels. The workflow uses no secrets: the self-hosted runners already have
registry push and cluster access, so a GitHub-hosted runner would need both added.

Without Kubernetes, the two images run anywhere: see "Build" in the [README](../README.md#build).

## Reference cluster: one-time setup (homelab repo)
- `cluster/apps/production/photosort/pvc.yaml` → `photosort-data-claim` (100Gi RBD). State, cache
  (~0.5 MB/image), thumbnails and exports live here.
- `photos-ro-claim` already exists in `production` (ROX CephFS of `/photos`, also mounted by Immich).
- `cluster/apps/production/ollama/helm-release.yaml`: chart ≥ 1.83.0, `ollama.models.pull:
  [qwen3-vl:4b-instruct]`, `OLLAMA_NUM_PARALLEL=1`, `OLLAMA_FLASH_ATTENTION=1`, `OLLAMA_KV_CACHE_TYPE=q8_0`.

- The chart creates `photosort-models` (RBD, kept on uninstall) and mounts it at `/models`; weights are
  copied there from the image on first start, so later images can drop the pre-fetch.

## App
```bash
make push VERSION=$(git describe --tags --always)   # buildx via the homelab remote builder → Harbor
make deploy VERSION=...                             # helm upgrade --install photosort .ci/chart -n production
kubectl -n production rollout status deploy/photosort-api
```
UI: https://photosort.adoah.dev (internal ingress, LAN/tailscale only). API health: `/api/health`.

## Gotchas
- The api Deployment has one replica and an RWO data volume; rollouts overlap briefly on the same node.
  Don't scale it. The job runner re-queues jobs that were running when a pod restarted.
- If `/api/health` reports `device: cpu`, the GPU isn't visible: check `runtimeClassName: nvidia` rendered
  and that the ollama pod is on the same node (the api pod follows the `nvidia.com/gpu` node label).
- Exports go to `/data/exports/<name>` on the data volume; copy them out with `kubectl cp` or mount the PVC.
