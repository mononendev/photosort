"""The torch/ultralytics pose detector photosort shipped with. Kept for the golden fixtures and the ONNX parity
report only; it needs the `ultralytics` extra and is never in the image."""
from __future__ import annotations
import threading
from typing import Optional

from PIL import Image


class UltralyticsDetector:
    """Thread-safe wrapper around a YOLO pose model. Loads lazily."""

    def __init__(self, weights: str = "yolo11n-pose.pt", imgsz: int = 1280, conf: float = 0.25, device: Optional[str] = None):
        self.name = weights.removesuffix(".pt")
        self.weights, self.imgsz, self.conf = weights, imgsz, conf
        self.device = device
        self._model = None
        self._lock = threading.Lock()

    def _load(self):
        from ultralytics import YOLO
        import torch
        from ..weights import weights_path
        if self.device is None:
            self.device = "cuda" if torch.cuda.is_available() else ("mps" if torch.backends.mps.is_available() else "cpu")
        path = weights_path(self.weights)
        # ultralytics downloads its own release assets by name when the file isn't on the models volume yet.
        self._model = YOLO(str(path) if path.exists() else self.weights)

    def __call__(self, im: Image.Image) -> list[dict]:
        with self._lock:
            if self._model is None:
                self._load()
            res = self._model.predict(im, imgsz=self.imgsz, conf=self.conf, classes=[0], verbose=False, device=self.device)[0]
        out = []
        if res.boxes is None or len(res.boxes) == 0:
            return out
        xyxy = res.boxes.xyxy.cpu().numpy()
        confs = res.boxes.conf.cpu().numpy()
        kxy = res.keypoints.xy.cpu().numpy() if res.keypoints is not None else None
        kcf = res.keypoints.conf.cpu().numpy() if (res.keypoints is not None and res.keypoints.conf is not None) else None
        for i in range(len(xyxy)):
            out.append({
                "box": xyxy[i].tolist(), "conf": float(confs[i]),
                "kp": kxy[i].tolist() if kxy is not None else None,
                "kpc": kcf[i].tolist() if kcf is not None else None,
            })
        return out
