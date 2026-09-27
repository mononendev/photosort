"""Where model weights live: $PHOTOSORT_MODELS (the models volume in the cluster), seeded from the image."""
from __future__ import annotations
import os
import shutil
import urllib.request
from pathlib import Path

SEED_WEIGHTS_DIR = Path(os.environ.get("PHOTOSORT_SEED_WEIGHTS", "/app/weights"))
WEIGHT_URLS = {
    "face_detection_yunet_2023mar.onnx":
        "https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx",
}


def models_dir() -> Path:
    d = Path(os.environ["PHOTOSORT_MODELS"]) if os.environ.get("PHOTOSORT_MODELS") else Path.cwd()
    d.mkdir(parents=True, exist_ok=True)
    return d


def weights_path(name: str) -> Path:
    """A weights file inside the models dir: already there, else copied from the image's seed, else downloaded
    (known URLs only). The path is returned either way; loading a missing file raises a clear error."""
    p = Path(name)
    if p.is_absolute():
        return p
    target = models_dir() / p
    if target.exists():
        return target
    seed = SEED_WEIGHTS_DIR / p
    if seed.exists():
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(seed, target)
        return target
    if p.name in WEIGHT_URLS:
        target.parent.mkdir(parents=True, exist_ok=True)
        tmp = target.with_suffix(target.suffix + ".part")
        urllib.request.urlretrieve(WEIGHT_URLS[p.name], tmp)
        tmp.replace(target)
    return target
