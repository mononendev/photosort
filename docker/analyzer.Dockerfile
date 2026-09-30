# The pixel stage: decoding, pose detection on ONNX Runtime, focus metrics. Listens on loopback by default; the
# chart runs it as an HPA-scaled pool on the pod network (ANALYZER_HOST=0.0.0.0, PHOTOSORT_PHOTOS_ROOT=/photos).
#
# POSE_MODELS are converted to ONNX in a build-only stage that has torch and ultralytics; neither ships. The image
# carries them (and the YuNet face model) as a seed the analyzer reads next to the models volume. For more, list them
# here (every name in `photosort models list`), or install onto the models volume where torch is available.
ARG PYTHON=3.13

FROM python:${PYTHON}-slim AS models
ARG POSE_MODELS="yolo11n-pose yolo26s-pose rtmo-s"
COPY --from=ghcr.io/astral-sh/uv:0.12 /uv /usr/local/bin/uv
# No uv cache here: torch is ~1.5 GB unpacked, and a cached copy beside it doubles that on the builder's disk.
ENV UV_NO_CACHE=1
RUN uv pip install --system --index-url https://download.pytorch.org/whl/cpu torch torchvision \
 && uv pip install --system ultralytics onnx onnxslim onnxruntime pillow pillow-heif rawpy numpy \
 && uv pip uninstall --system opencv-python \
 && uv pip install --system --reinstall opencv-python-headless  # ultralytics pulls the GUI build; they share cv2/
WORKDIR /build
# Only what the conversion imports, so edits to the rest of the package keep this stage (and torch) cached.
COPY analyzer/photosort_analyzer/__init__.py analyzer/photosort_analyzer/models.py \
     analyzer/photosort_analyzer/weights.py ./photosort_analyzer/
RUN PHOTOSORT_MODELS=/app/weights python -m photosort_analyzer.models get ${POSE_MODELS} \
 && PHOTOSORT_MODELS=/app/weights python -c "from photosort_analyzer.weights import weights_path; \
print(weights_path('face_detection_yunet_2023mar.onnx'))" \
 && ls -la /app/weights /app/weights/pose

# The runtime venv, from the lock. onnxruntime-gpu brings the CUDA execution provider (the RTX 4000 node).
FROM python:${PYTHON}-slim AS venv
ARG ORT=onnxruntime
COPY --from=ghcr.io/astral-sh/uv:0.12 /uv /usr/local/bin/uv
WORKDIR /app
COPY analyzer/pyproject.toml analyzer/uv.lock analyzer/.python-version ./
ENV UV_PROJECT_ENVIRONMENT=/app/.venv UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-install-project \
 && if [ "$ORT" != "onnxruntime" ]; then uv pip uninstall onnxruntime && uv pip install "$ORT"; fi

FROM python:${PYTHON}-slim AS production
RUN apt-get update && apt-get install -y --no-install-recommends libgomp1 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g 568 photosort && useradd -u 568 -g 568 -M -d /app -s /usr/sbin/nologin photosort
WORKDIR /app
COPY --from=venv /app/.venv /app/.venv
COPY --from=models /app/weights /app/weights
COPY analyzer/photosort_analyzer ./photosort_analyzer
ARG VERSION=dev
ENV PATH=/app/.venv/bin:$PATH PYTHONUNBUFFERED=1 PHOTOSORT_VERSION=${VERSION} \
    PHOTOSORT_MODELS=/models PHOTOSORT_SEED_WEIGHTS=/app/weights ANALYZER_HOST=127.0.0.1 ANALYZER_PORT=8090
USER 568:568
EXPOSE 8090
CMD ["python", "-m", "photosort_analyzer"]
