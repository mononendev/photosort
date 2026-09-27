"""Pose detection with ONNX Runtime: YOLO11/YOLOv8 (NMS here), YOLO26 (NMS-free head) and RTMO.

The YOLO pre- and post-processing reproduce ultralytics' predict for a PIL input step by step (BGR letterbox to a
multiple of 32 with gray 114, INTER_LINEAR, RGB/255; confidence filter, class-agnostic greedy NMS, boxes and keypoints
mapped back and clipped), so an exported model finds the same people the .pt did, up to runtime float noise.
"""
from __future__ import annotations
import os
import threading
from typing import Optional

import cv2
import numpy as np
from PIL import Image

_providers_lock = threading.Lock()
_providers: Optional[list[str]] = None


def providers() -> list[str]:
    """Execution providers to use: $PHOTOSORT_ORT_PROVIDERS (comma-separated), else CUDA when this onnxruntime has it,
    else CPU. (CoreML and the like are left opt-in: they compile per input shape and round differently.)"""
    global _providers
    with _providers_lock:
        if _providers is None:
            import onnxruntime as ort
            avail = ort.get_available_providers()
            want = [p.strip() for p in os.environ.get("PHOTOSORT_ORT_PROVIDERS", "").split(",") if p.strip()]
            if not want:
                want = ["CUDAExecutionProvider"] if "CUDAExecutionProvider" in avail else []
            _providers = [p for p in want if p in avail] + ["CPUExecutionProvider"]
        return _providers


def device() -> str:
    return "cuda" if "CUDAExecutionProvider" in providers() else "cpu"


def letterbox(bgr: np.ndarray, size: int, auto: bool, stride: int = 32, center: bool = True) -> tuple[np.ndarray, float, tuple[float, float]]:
    """ultralytics' LetterBox: scale to fit size x size, then pad (to a multiple of stride with auto) in gray 114.
    Returns the image, the gain and the (left, top) padding."""
    h, w = bgr.shape[:2]
    r = min(size / h, size / w)
    new_unpad = (round(w * r), round(h * r))
    dw, dh = size - new_unpad[0], size - new_unpad[1]
    if auto:
        dw, dh = np.mod(dw, stride), np.mod(dh, stride)
    if center:
        dw, dh = dw / 2, dh / 2
    if (w, h) != new_unpad:
        bgr = cv2.resize(bgr, new_unpad, interpolation=cv2.INTER_LINEAR)
    top, bottom = (int(round(dh - 0.1)) if center else 0), int(round(dh + 0.1))
    left, right = (int(round(dw - 0.1)) if center else 0), int(round(dw + 0.1))
    bgr = cv2.copyMakeBorder(bgr, top, bottom, left, right, cv2.BORDER_CONSTANT, value=(114, 114, 114))
    return bgr, r, (left, top)


def _unletterbox(xy: np.ndarray, in_shape: tuple[int, int], orig_shape: tuple[int, int]) -> np.ndarray:
    """ultralytics' scale_boxes / scale_coords with padding: undo the letterbox, in place, on [..., x, y, x, y...]
    columns (even x, odd y)."""
    gain = min(in_shape[0] / orig_shape[0], in_shape[1] / orig_shape[1])
    pad_x = round((in_shape[1] - orig_shape[1] * gain) / 2 - 0.1)
    pad_y = round((in_shape[0] - orig_shape[0] * gain) / 2 - 0.1)
    xy[..., 0::2] -= pad_x
    xy[..., 1::2] -= pad_y
    xy /= gain
    xy[..., 0::2] = xy[..., 0::2].clip(0, orig_shape[1])
    xy[..., 1::2] = xy[..., 1::2].clip(0, orig_shape[0])
    return xy


def nms(boxes: np.ndarray, scores: np.ndarray, iou: float) -> np.ndarray:
    """Greedy NMS as torchvision.ops.nms: highest score first, drop boxes whose IoU with a kept one exceeds iou."""
    order = np.argsort(-scores, kind="stable")
    x1, y1, x2, y2 = boxes.T
    area = (x2 - x1) * (y2 - y1)
    keep = []
    while order.size:
        i = order[0]
        keep.append(i)
        rest = order[1:]
        w = np.clip(np.minimum(x2[i], x2[rest]) - np.maximum(x1[i], x1[rest]), 0, None)
        h = np.clip(np.minimum(y2[i], y2[rest]) - np.maximum(y1[i], y1[rest]), 0, None)
        inter = w * h
        o = inter / (area[i] + area[rest] - inter)
        order = rest[~(o > iou)]
    return np.array(keep, dtype=int)


def _dets(boxes: np.ndarray, conf: np.ndarray, kpts: np.ndarray) -> list[dict]:
    return [{"box": boxes[i].tolist(), "conf": float(conf[i]), "kp": kpts[i, :, :2].tolist(), "kpc": kpts[i, :, 2].tolist()}
            for i in range(len(boxes))]


class OnnxPose:
    """One pose model. Callable on a PIL RGB image; thread-safe (ONNX Runtime sessions are)."""
    max_det = 300

    def __init__(self, name: str, path: str, family: str, imgsz: int, conf: float, iou: float):
        import onnxruntime as ort
        self.name, self.family, self.imgsz, self.conf, self.iou = name, family, imgsz, conf, iou
        opts = ort.SessionOptions()
        opts.log_severity_level = 3
        self.session = ort.InferenceSession(path, opts, providers=providers())
        self.input = self.session.get_inputs()[0].name
        self.device = device()

    def __call__(self, im: Image.Image) -> list[dict]:
        bgr = np.ascontiguousarray(np.asarray(im.convert("RGB"))[:, :, ::-1])
        if self.family == "rtmo":
            return self._rtmo(bgr)
        # ultralytics letterboxes a dynamic-shape model to the smallest multiple of 32 that fits (auto)
        lb, _, _ = letterbox(bgr, self.imgsz, auto=True)
        x = np.ascontiguousarray(lb[:, :, ::-1].transpose(2, 0, 1))[None].astype(np.float32) / 255.0
        out = self.session.run(None, {self.input: x})[0][0]
        in_shape, orig = lb.shape[:2], bgr.shape[:2]
        if self.family == "yolo_e2e":  # [300, 4 box xyxy + score + class + 51 keypoints]
            p = out[out[:, 4] > self.conf]
            p = p[np.argsort(-p[:, 4], kind="stable")][: self.max_det]
            boxes, conf, kp = p[:, :4].copy(), p[:, 4], p[:, 6:].reshape(-1, 17, 3).copy()
        else:  # yolo_nms: [4 box cxcywh + 1 class score + 51 keypoints, anchors]
            p = out.T
            p = p[p[:, 4] > self.conf]
            b = p[:, :4].copy()
            boxes = np.stack([b[:, 0] - b[:, 2] / 2, b[:, 1] - b[:, 3] / 2, b[:, 0] + b[:, 2] / 2, b[:, 1] + b[:, 3] / 2], 1)
            keep = nms(boxes, p[:, 4], self.iou)[: self.max_det]
            boxes, conf, kp = boxes[keep], p[keep, 4], p[keep, 5:].reshape(-1, 17, 3).copy()
        if len(boxes) == 0:
            return []
        _unletterbox(boxes, in_shape, orig)
        xy = kp[:, :, :2].reshape(len(kp), -1)
        kp[:, :, :2] = _unletterbox(xy, in_shape, orig).reshape(len(kp), 17, 2)
        return _dets(boxes, conf, kp)

    def _rtmo(self, bgr: np.ndarray) -> list[dict]:
        """RTMO as rtmlib runs it: top-left letterbox to 640x640 in gray 114, raw BGR 0-255; boxes and keypoints come
        out after the model's own NMS, in input pixels."""
        h, w = bgr.shape[:2]
        s = self.imgsz
        ratio = min(s / h, s / w)
        resized = cv2.resize(bgr, (int(w * ratio), int(h * ratio)), interpolation=cv2.INTER_LINEAR)
        padded = np.full((s, s, 3), 114, np.uint8)
        padded[: resized.shape[0], : resized.shape[1]] = resized
        x = np.ascontiguousarray(padded.transpose(2, 0, 1))[None].astype(np.float32)
        dets, kpts = self.session.run(None, {self.input: x})
        dets, kpts = dets[0], kpts[0]
        ok = dets[:, 4] > self.conf
        boxes, conf = dets[ok, :4] / ratio, dets[ok, 4]
        kp = kpts[ok].copy()
        kp[:, :, :2] /= ratio
        boxes[:, 0::2] = boxes[:, 0::2].clip(0, w)
        boxes[:, 1::2] = boxes[:, 1::2].clip(0, h)
        order = np.argsort(-conf, kind="stable")[: self.max_det]
        return _dets(boxes[order], conf[order], kp[order])
