# API + job worker: FastAPI, YOLO pose, sharpness scoring, and the Ollama client.
#
# The shipped image holds Python, the app and its weights; NOT torch or the other dependencies.
# docker/pydeps.sh (the entrypoint) installs requirements.lock.txt into a venv on $PHOTOSORT_PYDEPS
# (the models PVC in the cluster) the first time a pod starts with a new lock. Baked in, they were a
# ~2 GB layer re-pulled onto k8s-5's 48 GB root disk on every lock change, which caused DiskPressure
# evictions. torch is the CPU build (TORCH_INDEX); since it no longer touches node disk, the cu124
# index is now only a question of PVC size.
FROM python:3.11-slim AS base
ARG TORCH_INDEX=https://download.pytorch.org/whl/cpu
ENV PYTHONUNBUFFERED=1 PIP_DISABLE_PIP_VERSION_CHECK=1 PYTHONPATH=/app \
    PHOTOSORT_TORCH_INDEX=${TORCH_INDEX} YOLO_CONFIG_DIR=/tmp/yolo MPLCONFIGDIR=/tmp/mpl
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgomp1 ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 568 photosort && useradd -u 568 -g 568 -M -d /app -s /usr/sbin/nologin photosort
WORKDIR /app

# Build-only stage, never shipped: installs the lock with the same script the pods run (so a lock that
# doesn't install or import fails CI, not the rollout) and uses it to pre-fetch the weights seed. At
# runtime config.weights_path() copies the seed onto the models volume, so the pod needs no egress for it.
FROM base AS deps
COPY requirements.lock.txt docker/pydeps.sh ./
RUN --mount=type=cache,target=/root/.cache/pip \
    PHOTOSORT_PYDEPS=/opt/pydeps PIP_CACHE_DIR=/root/.cache/pip \
    ./pydeps.sh python -c "from ultralytics import YOLO; YOLO('/app/weights/yolo11n-pose.pt')"
COPY photosort/config.py /tmp/cfg/
RUN PHOTOSORT_MODELS=/app/weights python -c "import sys; sys.path.insert(0, '/tmp/cfg'); \
from config import weights_path; weights_path('face_detection_yunet_2023mar.onnx')" \
 && ls -la /app/weights

FROM base AS production
ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BUILD_TIME=
RUN chown 568:568 /app
COPY --chown=568:568 --from=deps /app/weights ./weights
COPY --chown=568:568 requirements.lock.txt docker/pydeps.sh pyproject.toml ./
COPY --chown=568:568 photosort ./photosort
ENV PHOTOSORT_VERSION=${VERSION} PHOTOSORT_GIT_SHA=${GIT_SHA} PHOTOSORT_BUILD_TIME=${BUILD_TIME} \
    PHOTOSORT_WORKDIR=/data PHOTOSORT_PHOTOS=/photos PHOTOSORT_MODELS=/app/weights PORT=8080 \
    NVIDIA_VISIBLE_DEVICES=all NVIDIA_DRIVER_CAPABILITIES=compute,utility
USER 568:568
EXPOSE 8080
ENTRYPOINT ["/app/pydeps.sh"]
CMD ["python", "-m", "photosort", "web"]
