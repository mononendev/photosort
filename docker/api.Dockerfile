# API + job worker: FastAPI, YOLO pose (torch, CUDA via the nvidia runtime), sharpness scoring,
# and the Ollama client. Run with the nvidia runtime class and NVIDIA_VISIBLE_DEVICES=all to share
# the GPU with Ollama (no nvidia.com/gpu resource request); falls back to CPU otherwise.
FROM python:3.11-slim AS base
ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1 \
    YOLO_CONFIG_DIR=/tmp/yolo MPLCONFIGDIR=/tmp/mpl
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 libgomp1 ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 568 photosort && useradd -u 568 -g 568 -M -d /app -s /usr/sbin/nologin photosort
WORKDIR /app

FROM base AS deps
# CUDA 12.4 wheels bundle the CUDA runtime; the driver comes from the host via the nvidia runtime.
RUN pip install torch torchvision --index-url https://download.pytorch.org/whl/cu124
COPY pyproject.toml ./
COPY photosort ./photosort
RUN pip install .
# Pre-fetch weights so the pod needs no egress at runtime.
# ultralytics pulls in opencv-python, which shares the cv2/ directory with the headless build
# (a partial uninstall breaks both): remove both, then install headless once.
RUN pip uninstall -y opencv-python opencv-python-headless && pip install opencv-python-headless \
 && python -c "import cv2; print('cv2', cv2.__version__)" \
 && mkdir -p /app/weights && cd /app/weights && python -c "from ultralytics import YOLO; YOLO('yolo11n-pose.pt')" && ls -la /app/weights

FROM deps AS production
ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BUILD_TIME=
ENV PHOTOSORT_VERSION=${VERSION} PHOTOSORT_GIT_SHA=${GIT_SHA} PHOTOSORT_BUILD_TIME=${BUILD_TIME} \
    PHOTOSORT_WORKDIR=/data PHOTOSORT_PHOTOS=/photos PORT=8080 \
    NVIDIA_VISIBLE_DEVICES=all NVIDIA_DRIVER_CAPABILITIES=compute,utility
RUN chown -R 568:568 /app
USER 568:568
WORKDIR /app/weights
EXPOSE 8080
CMD ["python", "-m", "photosort", "web"]
