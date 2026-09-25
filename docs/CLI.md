# Running stage 1 on the k8s GPU node

```bash
docker build -t REGISTRY/photosort:latest -f deploy/Dockerfile .
docker push REGISTRY/photosort:latest
# edit deploy/job.yaml: image, PVC names (photos read-only, data read-write)
kubectl apply -f deploy/job.yaml
kubectl logs -f job/photosort-local
```

Throughput on the node is bound by CPU JPEG decode, not the GPU: give the container
as many cores as you can and set `--workers` to about that number. When the job is done,
mount or copy `/data/photosort_work` to your laptop and continue with
`photosort --workdir <that dir> estimate|submit|poll|sort`. Image paths in the database are
recorded as seen inside the container (`/photos/...`); `sort` needs the same paths, so
either mount the photos at `/photos` locally too or run `sort` in-cluster.

# Local vision model (no API cost) on the same node

```bash
kubectl apply -f deploy/ollama.yaml
kubectl port-forward svc/ollama 11434:11434     # or use the NodePort
photosort submit --backend ollama --base-url http://127.0.0.1:11434 --model qwen3-vl:4b \
    --concurrency 3 --skip-local-tier0
```

`submit` with the ollama backend runs synchronously and stores each result as it arrives, so it
can be interrupted and resumed. Match `--concurrency` to `OLLAMA_NUM_PARALLEL`. vLLM is not an
option on this card: it dropped Turing (compute capability 7.5) for Qwen3-VL. Ollama/llama.cpp
run GGUF on Turing fine.
