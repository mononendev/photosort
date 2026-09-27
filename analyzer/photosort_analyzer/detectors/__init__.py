"""Pose detectors by name. Each is callable on a PIL RGB image and returns detections in that image's pixels:
[{"box": [x0, y0, x1, y1], "conf": float, "kp": [[x, y]] * 17 | None, "kpc": [c] * 17 | None}], COCO-17 order.

Names come from the model store (see models.CATALOG: yolo11s-pose, yolo26m-pose, rtmo-l, ...). A name ending in .pt
runs the original ultralytics/torch model instead; that needs the `ultralytics` extra and is for parity checks only.
"""
from __future__ import annotations
import threading

from .. import models

_lock = threading.Lock()
_cache: dict[tuple, object] = {}

# The config default before the ONNX move; rows analyzed with it say "yolo11n-pose.pt".
LEGACY = {"yolo11n-pose.pt": "yolo11n-pose"}


def device() -> str:
    """Where detection runs by default."""
    from .onnx_ import device as d
    return d()


def installed() -> list[dict]:
    """Pose models available without a download."""
    return [{"name": n, "family": e["family"], "imgsz": e["imgsz"], "license": e.get("license"), "bytes": e.get("bytes")}
            for n, e in sorted(models.manifest().items())]


def get(model: str, imgsz: int | None = None, conf: float = 0.25, iou: float = 0.7, device: str | None = None):
    """A detector for `model`, loaded once per (model, imgsz, conf, iou) and shared across threads. With PHOTOSORT_TORCH
    unset, the old default "yolo11n-pose.pt" maps onto its ONNX export."""
    import os
    if model in LEGACY and not os.environ.get("PHOTOSORT_TORCH"):
        model = LEGACY[model]
    key = (model, imgsz, conf, iou, device)
    with _lock:
        det = _cache.get(key)
        if det is None:
            if model.endswith(".pt"):
                from .ultralytics_ import UltralyticsDetector
                det = UltralyticsDetector(model, imgsz or 1280, conf, device)
            else:
                spec = models.manifest().get(model)
                if spec is None:
                    known = ", ".join(sorted(models.manifest())) or "none"
                    raise ValueError(f"pose model {model!r} is not installed (installed: {known}); "
                                     f"`photosort models get {model}` fetches it")
                from .onnx_ import OnnxPose
                size = spec["imgsz"] if spec["family"] == "rtmo" else (imgsz or spec["imgsz"])
                det = OnnxPose(model, spec["path"], spec["family"], size, conf, iou)
            _cache[key] = det
        return det
