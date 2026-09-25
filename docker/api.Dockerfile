# API + job worker: FastAPI, YOLO pose, sharpness scoring, and the Ollama client.
# torch is the CPU build on purpose: the CUDA wheels make the image ~5 GB, which evicted pods on
# k8s-5 (48 GB ephemeral disk) during pulls. YOLO11n-pose on CPU costs ~0.2-0.3 s/image with a few
# cores, small next to the vision model's ~10 s/image. Switch TORCH_INDEX to the cu124 index to go
# back to GPU detection if the node gets more disk.
FROM python:3.11-slim AS base
ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1 \
    YOLO_CONFIG_DIR=/tmp/yolo MPLCONFIGDIR=/tmp/mpl
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 libgomp1 ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 568 photosort && useradd -u 568 -g 568 -M -d /app -s /usr/sbin/nologin photosort
WORKDIR /app

FROM base AS deps
ARG TORCH_INDEX=https://download.pytorch.org/whl/cpu
RUN pip install torch torchvision --index-url ${TORCH_INDEX}
COPY pyproject.toml ./
COPY photosort ./photosort
RUN pip install .
# Pre-fetch weights as a seed: at runtime config.weights_path() copies them onto the models volume
# (PHOTOSORT_MODELS) on first use, so the pod needs no egress and later image pulls stay small.
# ultralytics pulls in opencv-python, which shares the cv2/ directory with the headless build
# (a partial uninstall breaks both): remove both, then install headless once.
RUN pip uninstall -y opencv-python opencv-python-headless && pip install opencv-python-headless \
 && python -c "import cv2; print('cv2', cv2.__version__)" \
 && mkdir -p /app/weights && cd /app/weights && python -c "from ultralytics import YOLO; YOLO('yolo11n-pose.pt')" \
 && python -c "from photosort.config import weights_path; weights_path('face_detection_yunet_2023mar.onnx')" \
 && ls -la /app/weights

FROM deps AS production
ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BUILD_TIME=
ENV PHOTOSORT_VERSION=${VERSION} PHOTOSORT_GIT_SHA=${GIT_SHA} PHOTOSORT_BUILD_TIME=${BUILD_TIME} \
    PHOTOSORT_WORKDIR=/data PHOTOSORT_PHOTOS=/photos PHOTOSORT_MODELS=/app/weights PORT=8080 \
    NVIDIA_VISIBLE_DEVICES=all NVIDIA_DRIVER_CAPABILITIES=compute,utility
RUN chown -R 568:568 /app
USER 568:568
WORKDIR /app
EXPOSE 8080
CMD ["python", "-m", "photosort", "web"]
