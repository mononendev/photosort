# Deploying photosort

Mirrors stasharr: the app repo owns its Helm chart and images; the homelab repo owns cluster-level
resources (volumes, Ollama).

## One-time cluster setup (homelab repo)
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
