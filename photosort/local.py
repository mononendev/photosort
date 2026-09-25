"""Stage 1: person/pose detection + native-resolution sharpness scoring (no network)."""
from __future__ import annotations
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

import cv2
import numpy as np
from PIL import Image

from . import images as I

# COCO keypoint indices used by YOLO pose models
NOSE, LEYE, REYE, LEAR, REAR, LSHO, RSHO, LHIP, RHIP = 0, 1, 2, 3, 4, 5, 6, 11, 12
HEAD_KP = [NOSE, LEYE, REYE, LEAR, REAR]
MIN_REGION_PX = 40


def sharpness(gray: np.ndarray) -> Optional[float]:
    """Contrast-normalized Laplacian variance on a float32 grayscale array in [0,1].

    A light Gaussian blur first suppresses sensor noise (which otherwise inflates the
    metric at high ISO). Normalizing by the region's own variance makes a low-contrast
    but sharp region comparable to a high-contrast one. Higher = sharper.
    """
    if gray is None or min(gray.shape[:2]) < MIN_REGION_PX:
        return None
    h, w = gray.shape[:2]
    if max(h, w) > 512:
        s = 512 / max(h, w)
        gray = cv2.resize(gray, (max(1, round(w * s)), max(1, round(h * s))), interpolation=cv2.INTER_AREA)
    g = cv2.GaussianBlur(gray, (0, 0), 1.0)
    lap = cv2.Laplacian(g, cv2.CV_32F, ksize=3)
    return float(lap.var() / (g.var() + 2e-3))


def _clamp_box(b, W, H):
    x0, y0, x1, y1 = b
    return (max(0, int(x0)), max(0, int(y0)), min(W, int(x1)), min(H, int(y1)))


def _region(gray, b):
    x0, y0, x1, y1 = b
    if x1 - x0 < MIN_REGION_PX or y1 - y0 < MIN_REGION_PX:
        return None
    return gray[y0:y1, x0:x1]


class Detector:
    """Thread-safe wrapper around a YOLO pose model. Loads lazily."""

    def __init__(self, weights: str = "yolo11n-pose.pt", imgsz: int = 1280, conf: float = 0.25, device: Optional[str] = None):
        self.weights, self.imgsz, self.conf = weights, imgsz, conf
        self.device = device
        self._model = None
        self._lock = threading.Lock()

    def _load(self):
        from ultralytics import YOLO
        import torch
        from .config import weights_path
        if self.device is None:
            self.device = "cuda" if torch.cuda.is_available() else ("mps" if torch.backends.mps.is_available() else "cpu")
        self._model = YOLO(str(weights_path(self.weights)))

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


def _person_regions(det: dict, scale: float, W: int, H: int) -> dict:
    """Derive head / torso / upper-body boxes (full-res pixels) from a detection."""
    x0, y0, x1, y1 = [v * scale for v in det["box"]]
    bw, bh = x1 - x0, y1 - y0
    kp, kpc = det.get("kp"), det.get("kpc")

    def pt(i):
        if kp is None or kpc is None or kpc[i] < 0.3:
            return None
        return (kp[i][0] * scale, kp[i][1] * scale)

    head_pts = [p for p in (pt(i) for i in HEAD_KP) if p]
    lsho, rsho, lhip, rhip = pt(LSHO), pt(RSHO), pt(LHIP), pt(RHIP)
    shoulder_w = abs(lsho[0] - rsho[0]) if (lsho and rsho) else None

    if head_pts:
        cx = sum(p[0] for p in head_pts) / len(head_pts)
        cy = sum(p[1] for p in head_pts) / len(head_pts)
        side = max(0.9 * shoulder_w if shoulder_w else 0, 0.30 * bw, 0.14 * bh)
        side = min(side, 0.9 * bw + 0.2 * bh)
        head = (cx - side / 2, cy - side / 2, cx + side / 2, cy + side / 2)
        head_src = "keypoints"
    else:
        # Fallback: assume the head is at the top of an upright box.
        side = max(0.5 * bw, 0.22 * bh)
        head = (x0 + bw / 2 - side / 2, y0, x0 + bw / 2 + side / 2, y0 + side)
        head_src = "box_top"

    sho_y = (lsho[1] + rsho[1]) / 2 if (lsho and rsho) else y0 + 0.22 * bh
    hip_y = (lhip[1] + rhip[1]) / 2 if (lhip and rhip) else y0 + 0.58 * bh
    torso = (x0, sho_y, x1, max(hip_y, sho_y + 0.15 * bh))

    # Upper-body crop for the VLM: from above the head down to the hips, widened for context.
    top = min(head[1], y0) - 0.08 * bh
    bottom = min(y1, max(hip_y, head[3] + 0.3 * bh))
    ch = bottom - top
    cw = max(bw * 1.3, ch * 0.8)
    cx = (x0 + x1) / 2
    upper = (cx - cw / 2, top, cx + cw / 2, bottom)

    return {
        "box": _clamp_box((x0, y0, x1, y1), W, H),
        "head": _clamp_box(head, W, H), "head_src": head_src,
        "torso": _clamp_box(torso, W, H),
        "upper": _clamp_box(upper, W, H),
        "conf": det["conf"],
    }


def local_tier(primary: Optional[dict], others: list[dict], thr: dict) -> tuple[int, str]:
    if primary is None:
        return 0, "no_people"
    s = primary.get("sharp_head") or primary.get("sharp_body")
    if s is None:
        return 0, "subject_too_small"
    if s >= thr["tier2_min"]:
        return 2, "primary_head_sharp"
    best_other = max((p.get("sharp_head") or p.get("sharp_body") or 0) for p in others) if others else 0
    if best_other >= thr["tier2_min"]:
        return 1, "secondary_person_sharp"
    if s >= thr["tier1_min"]:
        return 1, "primary_soft"
    return 0, "nothing_sharp"


@dataclass
class LocalResult:
    data: dict
    frame_jpeg: bytes
    crop_jpeg: Optional[bytes] = None


def analyze(path: Path, cfg: dict, detector: Detector) -> LocalResult:
    im = I.load_rgb(path)
    W, H = im.size
    small = I.resize_long_edge(im, cfg["detect_long_edge"])
    scale = W / small.size[0]
    dets = [d for d in detector(small)
            if ((d["box"][2] - d["box"][0]) * (d["box"][3] - d["box"][1]) * scale * scale) / (W * H) >= cfg["min_person_frac"]]

    gray = np.asarray(im.convert("L"), dtype=np.float32) / 255.0
    people = []
    mask = np.ones(gray.shape, dtype=bool)
    for d in dets:
        r = _person_regions(d, scale, W, H)
        bx = r["box"]
        area_frac = ((bx[2] - bx[0]) * (bx[3] - bx[1])) / (W * H)
        cx, cy = (bx[0] + bx[2]) / 2 / W, (bx[1] + bx[3]) / 2 / H
        center_dist = float(np.hypot(cx - 0.5, cy - 0.5) / 0.7071)
        r.update({
            "area_frac": area_frac, "center": [round(cx, 3), round(cy, 3)], "center_dist": round(center_dist, 3),
            "sharp_head": sharpness(_region(gray, r["head"])),
            "sharp_torso": sharpness(_region(gray, r["torso"])),
            "sharp_body": sharpness(_region(gray, r["box"])),
        })
        r["priority"] = area_frac * (1 - 0.5 * center_dist) * (0.5 + 0.5 * r["conf"])
        people.append(r)
        mask[bx[1]:bx[3], bx[0]:bx[2]] = False

    # Background sharpness: whole frame downscaled, persons masked out.
    s = 1024 / max(W, H)
    g_small = cv2.resize(gray, (max(1, round(W * s)), max(1, round(H * s))), interpolation=cv2.INTER_AREA)
    m_small = cv2.resize(mask.astype(np.uint8), g_small.shape[::-1], interpolation=cv2.INTER_NEAREST).astype(bool)
    gb = cv2.GaussianBlur(g_small, (0, 0), 1.0)
    lap = cv2.Laplacian(gb, cv2.CV_32F, ksize=3)
    bg_sharp = float(lap[m_small].var() / (gb[m_small].var() + 2e-3)) if m_small.sum() > 5000 else None
    global_sharp = float(lap.var() / (gb.var() + 2e-3))

    people.sort(key=lambda p: -p["priority"])
    primary = people[0] if people else None
    tier, reason = local_tier(primary, people[1:], cfg["focus"])

    frame = I.to_jpeg(I.resize_long_edge(im, cfg["frame_long_edge"]), cfg["frame_quality"])
    crop_jpeg, crop_used = None, None
    if primary:
        crop_im, crop_used = I.crop_box(im, primary["upper"], cfg["crop_pad"], cfg["crop_size"])
        crop_jpeg = I.to_jpeg(crop_im, cfg["crop_quality"])

    def rnd(v):
        return None if v is None else round(v, 4)

    data = {
        "width": W, "height": H, "orientation": "portrait" if H > W else "landscape",
        "n_people": len(people),
        "people": [{k: (rnd(v) if k.startswith("sharp") else v) for k, v in p.items() if k != "priority"} for p in people[:6]],
        "bg_sharp": rnd(bg_sharp), "global_sharp": rnd(global_sharp),
        "primary_head_sharp": rnd(primary["sharp_head"]) if primary else None,
        "primary_body_sharp": rnd(primary["sharp_body"]) if primary else None,
        "crop_box": crop_used,
        "local_tier": tier, "local_reason": reason,
    }
    return LocalResult(data, frame, crop_jpeg)


THUMB_LONG_EDGE = 400


def write_cache(cache_dir: Path, img_id: int, res: "LocalResult"):
    (cache_dir / f"{img_id}.jpg").write_bytes(res.frame_jpeg)
    cp = cache_dir / f"{img_id}_crop.jpg"
    if res.crop_jpeg:
        cp.write_bytes(res.crop_jpeg)
    elif cp.exists():
        cp.unlink()
    import io
    thumb = I.resize_long_edge(Image.open(io.BytesIO(res.frame_jpeg)), THUMB_LONG_EDGE)
    (cache_dir / f"{img_id}_thumb.jpg").write_bytes(I.to_jpeg(thumb, 80))


def run_local(db, cfg: dict, cache_dir: Path, ids_paths: list[tuple[int, str]], device: Optional[str] = None,
              progress=None, should_stop=None, detector: Optional[Detector] = None):
    """Analyze images concurrently and store results. progress.update(1) per image; should_stop() aborts."""
    cache_dir.mkdir(parents=True, exist_ok=True)
    det = detector or Detector(cfg["detect_model"], cfg["detect_long_edge"], cfg["detect_conf"], device)
    det(Image.new("RGB", (64, 64)))  # warm up / download weights before threads start

    def work(img_id, path):
        if should_stop and should_stop():
            return img_id, None
        res = analyze(Path(path), cfg, det)
        write_cache(cache_dir, img_id, res)
        return img_id, res.data

    n_ok = n_err = 0
    with ThreadPoolExecutor(max_workers=cfg["workers"]) as ex:
        futs = {ex.submit(work, i, p): (i, p) for i, p in ids_paths}
        for f in as_completed(futs):
            i, p = futs[f]
            try:
                img_id, data = f.result()
                if data is None:
                    continue  # stopped
                db.set_local(img_id, data)
                n_ok += 1
            except Exception as e:  # keep going; record the failure
                db.set_local(i, None, f"local: {type(e).__name__}: {e}")
                n_err += 1
            if progress:
                progress.update(1)
    return n_ok, n_err
