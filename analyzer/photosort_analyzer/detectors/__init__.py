"""Pose detectors by name. Each is callable on a PIL RGB image and returns detections in that image's pixels:
[{"box": [x0, y0, x1, y1], "conf": float, "kp": [[x, y]] * 17 | None, "kpc": [c] * 17 | None}], COCO-17 order."""
from __future__ import annotations
import threading

_lock = threading.Lock()
_cache: dict[tuple, object] = {}


def device() -> str:
    """Where detection runs by default."""
    return "cpu"


def installed() -> list[dict]:
    """Pose models available without a download."""
    return []


def get(model: str, imgsz: int = 1280, conf: float = 0.25, iou: float = 0.7, device: str | None = None):
    """A detector for `model`, loaded once per (model, imgsz, conf, iou, device) and shared across threads.
    Names ending in .pt use ultralytics (the `ultralytics` extra)."""
    key = (model, imgsz, conf, iou, device)
    with _lock:
        det = _cache.get(key)
        if det is None:
            if model.endswith(".pt"):
                from .ultralytics_ import UltralyticsDetector
                det = UltralyticsDetector(model, imgsz, conf, device)
            else:
                raise ValueError(f"unknown pose model {model!r}")
            _cache[key] = det
        return det
