"""Stage 1: person/pose detection + native-resolution sharpness scoring (no network)."""
from __future__ import annotations
import json
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

import cv2
import numpy as np
from PIL import Image

from . import images as I
from . import af as A
from . import exif as X

# COCO keypoint indices used by YOLO pose models
NOSE, LEYE, REYE, LEAR, REAR, LSHO, RSHO, LHIP, RHIP = 0, 1, 2, 3, 4, 5, 6, 11, 12
HEAD_KP = [NOSE, LEYE, REYE, LEAR, REAR]
MIN_REGION_PX = 40
EYE_MIN_PX = 24      # an eye band is small by nature; below this there is nothing to judge
FACE_WEIGHTS = "face_detection_yunet_2023mar.onnx"


EPS = 2e-3   # contrast floor in the Laplacian normalization: keeps flat regions from dividing by ~0


def _fit512(gray: np.ndarray) -> np.ndarray:
    h, w = gray.shape[:2]
    if max(h, w) > 512:
        s = 512 / max(h, w)
        gray = cv2.resize(gray, (max(1, round(w * s)), max(1, round(h * s))), interpolation=cv2.INTER_AREA)
    return gray


def sharpness_parts(gray: np.ndarray, min_px: int = MIN_REGION_PX) -> Optional[dict]:
    """The terms of the sharpness metric: {"lap_var", "gray_var", "px": [w, h] measured at}, or None."""
    if gray is None or min(gray.shape[:2]) < min_px:
        return None
    g = cv2.GaussianBlur(_fit512(gray), (0, 0), 1.0)
    lap = cv2.Laplacian(g, cv2.CV_32F, ksize=3)
    return {"lap_var": float(lap.var()), "gray_var": float(g.var()), "px": [g.shape[1], g.shape[0]]}


def sharpness(gray: np.ndarray, min_px: int = MIN_REGION_PX) -> Optional[float]:
    """Contrast-normalized Laplacian variance on a float32 grayscale array in [0,1].

    A light Gaussian blur first suppresses sensor noise (which otherwise inflates the
    metric at high ISO). Normalizing by the region's own variance makes a low-contrast
    but sharp region comparable to a high-contrast one. Higher = sharper.
    """
    t = sharpness_parts(gray, min_px)
    return None if t is None else t["lap_var"] / (t["gray_var"] + EPS)


def hf_parts(gray: np.ndarray, band=(0.25, 0.75), floor: float = 0.03, min_px: int = EYE_MIN_PX) -> Optional[dict]:
    """The terms of the FFT ratio: {"band_e", "total_e"} (energy in `band`, energy from `floor` up), or None."""
    if gray is None or min(gray.shape[:2]) < min_px:
        return None
    gray = _fit512(gray)
    h, w = gray.shape[:2]
    g = (gray - gray.mean()) * np.outer(np.hanning(h), np.hanning(w)).astype(np.float32)
    p = np.abs(np.fft.rfft2(g)) ** 2
    r = np.hypot(np.fft.fftfreq(h)[:, None], np.fft.rfftfreq(w)[None, :]) / 0.5
    total = float(p[(r >= floor) & (r <= band[1])].sum())
    if total <= 1e-12:
        return None
    return {"band_e": float(p[(r >= band[0]) & (r <= band[1])].sum()), "total_e": total}


def hf_ratio(gray: np.ndarray, band=(0.25, 0.75), floor: float = 0.03, min_px: int = EYE_MIN_PX) -> Optional[float]:
    """Share of spectral energy in the upper-mid frequency band, on a float32 grayscale array in [0,1].

    Frequencies are radial, as a fraction of Nyquist. Energy in `band` is divided by the energy from
    `floor` (which drops DC and the slow gradients that say nothing about focus) up to the same upper
    edge. Slight defocus attenuates this band well before it moves the Laplacian much. The top of the
    spectrum, where high-ISO noise and JPEG ringing live, is left out of both sums. A Hann window keeps
    the region edges from leaking into it.
    """
    t = hf_parts(gray, band, floor, min_px)
    return None if t is None else t["band_e"] / t["total_e"]


def _sig(t: Optional[dict]) -> Optional[dict]:
    """Round stored metric terms to 4 significant figures (they span many orders of magnitude)."""
    return None if t is None else {k: (float(f"{v:.4g}") if isinstance(v, float) else v) for k, v in t.items()}


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


class FaceLandmarks:
    """OpenCV YuNet face detector (box + eyes/nose/mouth corners), run on a native-resolution crop around
    the pose head box. One net per thread (cv2.dnn nets are not safe to share); loads lazily."""

    def __init__(self, weights: str = FACE_WEIGHTS, conf: float = 0.6):
        self.weights, self.conf = weights, conf
        self._tls = threading.local()

    def _net(self):
        if getattr(self._tls, "net", None) is None:
            from .config import weights_path
            self._tls.net = cv2.FaceDetectorYN.create(str(weights_path(self.weights)), "", (320, 320), self.conf)
        return self._tls.net

    def face(self, rgb: np.ndarray, head: tuple, W: int, H: int) -> Optional[dict]:
        """The face nearest the head box center, in full-frame pixels, or None: {"eyes": [right, left],
        "score", "box": [x0, y0, x1, y1], "lm": [right eye, left eye, nose, right mouth, left mouth],
        "search": the window searched}. The window is the head box grown to 3x so a coarse box still
        contains the face."""
        x0, y0, x1, y1 = head
        side = max(x1 - x0, y1 - y0)
        cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
        sx0, sy0, sx1, sy1 = _clamp_box((cx - 1.5 * side, cy - 1.5 * side, cx + 1.5 * side, cy + 1.5 * side), W, H)
        if sx1 - sx0 < 16 or sy1 - sy0 < 16:
            return None
        crop = rgb[sy0:sy1, sx0:sx1]
        # Detection only (the metrics read the original pixels): bring the window to a size YuNet likes.
        k = min(640 / max(crop.shape[:2]), max(1.0, 192 / max(crop.shape[:2])))
        if k != 1:
            crop = cv2.resize(crop, (max(1, round(crop.shape[1] * k)), max(1, round(crop.shape[0] * k))),
                              interpolation=cv2.INTER_AREA if k < 1 else cv2.INTER_CUBIC)
        net = self._net()
        net.setInputSize((crop.shape[1], crop.shape[0]))
        _, faces = net.detect(np.ascontiguousarray(crop[:, :, ::-1]))
        if faces is None or len(faces) == 0:
            return None
        hx, hy = (cx - sx0) * k, (cy - sy0) * k
        dist = lambda f: np.hypot((f[4] + f[6]) / 2 - hx, (f[5] + f[7]) / 2 - hy)
        f = min(faces, key=dist)
        if dist(f) > side * k:  # nearest face belongs to someone else
            return None
        to_full = lambda x, y: (sx0 + float(x) / k, sy0 + float(y) / k)
        lm = [to_full(f[i], f[i + 1]) for i in range(4, 14, 2)]
        bx0, by0 = to_full(f[0], f[1])
        return {"eyes": lm[:2], "score": float(f[14]), "lm": lm,
                "box": [bx0, by0, bx0 + float(f[2]) / k, by0 + float(f[3]) / k], "search": [sx0, sy0, sx1, sy1]}

    def eyes(self, rgb: np.ndarray, head: tuple, W: int, H: int) -> Optional[tuple]:
        """((x, y) right eye, (x, y) left eye, score) for the face nearest the head box, or None."""
        f = self.face(rgb, head, W, H)
        return (f["eyes"][0], f["eyes"][1], f["score"]) if f else None


def eye_band(e1, e2, W: int, H: int) -> Optional[tuple]:
    """Box over both eyes, lids and lashes: half an inter-eye distance beyond each eye, 0.4 above/below."""
    iod = float(np.hypot(e1[0] - e2[0], e1[1] - e2[1]))
    if iod < 8:
        return None
    xs, ys = (e1[0], e2[0]), (e1[1], e2[1])
    return _clamp_box((min(xs) - 0.5 * iod, min(ys) - 0.4 * iod, max(xs) + 0.5 * iod, max(ys) + 0.4 * iod), W, H)


def _pose_eyes(det: dict, scale: float, min_conf: float = 0.5):
    kp, kpc = det.get("kp"), det.get("kpc")
    if kp is None or kpc is None or kpc[LEYE] < min_conf or kpc[REYE] < min_conf:
        return None
    return (kp[REYE][0] * scale, kp[REYE][1] * scale), (kp[LEYE][0] * scale, kp[LEYE][1] * scale)


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
        # COCO-17 pose keypoints as [x, y, confidence] in full-res pixels, for the UI overlay
        "kp": [[round(kp[i][0] * scale), round(kp[i][1] * scale), round(float(kpc[i]), 3)] for i in range(len(kp))]
        if kp is not None and kpc is not None else None,
        "head": _clamp_box(head, W, H), "head_src": head_src,
        "torso": _clamp_box(torso, W, H),
        "upper": _clamp_box(upper, W, H),
        "conf": det["conf"],
    }


def _grade(p: dict, thr: dict, k: float = 1.0) -> Optional[int]:
    """2/1/0 for one person with every threshold multiplied by k, or None when nothing is measurable.

    With an eye band, both the Laplacian and the FFT ratio on it must clear their thresholds (the FFT
    check is skipped when use_hf is off or the band had no FFT value). Without one, the head box
    Laplacian is judged against the head thresholds, as before."""
    if thr.get("use_eyes", True) and p.get("sharp_eye") is not None and "eye_tier2_min" in thr:
        lap, hf = p["sharp_eye"], p.get("hf_eye")
        use_hf = thr.get("use_hf", True) and hf is not None and "hf_tier2_min" in thr

        def ok(lvl):
            return lap >= k * thr[f"eye_tier{lvl}_min"] and (not use_hf or hf >= k * thr[f"hf_tier{lvl}_min"])
    else:
        s = p.get("sharp_head") or p.get("sharp_body")
        if s is None:
            return None

        def ok(lvl):
            return s >= k * thr[f"tier{lvl}_min"]
    return 2 if ok(2) else 1 if ok(1) else 0


def local_tier(primary: Optional[dict], others: list[dict], thr: dict, prior: Optional[dict] = None,
               shake_margin: float = 1.5) -> tuple[int, str]:
    """Tier from measured sharpness; the EXIF prior only demotes a *borderline* tier 2 shot at a slow shutter
    (a clearly sharp subject wins, e.g. a well-panned rider)."""
    if primary is None:
        return 0, "no_people"
    g = _grade(primary, thr)
    if g is None:
        return 0, "subject_too_small"
    on_eyes = thr.get("use_eyes", True) and primary.get("sharp_eye") is not None and "eye_tier2_min" in thr
    if g == 2:
        if prior and prior.get("motion_risk") == "high" and _grade(primary, thr, shake_margin) < 2:
            return 1, "borderline_sharp_slow_shutter"
        return 2, "primary_eyes_sharp" if on_eyes else "primary_head_sharp"
    if g == 1:  # the primary's own grade names the reason; a sharp bystander only rescues a missed primary
        return 1, "primary_eyes_soft" if on_eyes else "primary_soft"
    if any(_grade(o, thr) == 2 for o in others):
        return 1, "secondary_person_sharp"
    return 0, "nothing_sharp"


@dataclass
class LocalResult:
    data: dict
    frame_jpeg: bytes
    crop_jpeg: Optional[bytes] = None


def _eye_metrics(r: dict, det: dict, scale: float, rgb: np.ndarray, gray: np.ndarray, W: int, H: int,
                 faces: Optional[FaceLandmarks]):
    """Locate the eyes (face landmarks first, then confident pose eye keypoints) and score the eye band."""
    found = faces.face(rgb, r["head"], W, H) if faces is not None else None
    if found:
        e1, e2, src = found["eyes"][0], found["eyes"][1], "face"
    else:
        pe = _pose_eyes(det, scale)
        e1, e2, src = (pe[0], pe[1], "pose") if pe else (None, None, None)
    band = eye_band(e1, e2, W, H) if e1 else None
    region = gray[band[1]:band[3], band[0]:band[2]] if band else None
    lt = sharpness_parts(region, EYE_MIN_PX) if band else None
    ht = hf_parts(region) if lt else None
    lap = lt["lap_var"] / (lt["gray_var"] + EPS) if lt else None
    r.update({"eyes": [[round(e1[0]), round(e1[1])], [round(e2[0]), round(e2[1])]] if lap is not None else None,
              "eye_src": src if lap is not None else None, "eye": band if lap is not None else None,
              "sharp_eye": lap, "hf_eye": ht["band_e"] / ht["total_e"] if ht else None})
    r.setdefault("terms", {})["eye"] = _sig({**lt, **(ht or {})}) if lt else None
    # What the face model saw, kept for the UI overlay even when the band ended up unusable.
    r["face"] = None if not found else {
        "box": [round(v) for v in found["box"]], "search": list(found["search"]), "score": round(found["score"], 3),
        "lm": [[round(x), round(y)] for x, y in found["lm"]]}


def pick_primary(people: list[dict], af: Optional[dict], cfg: dict) -> str:
    """Order people in place, primary first; returns what chose the primary ("af" or "priority").

    People are ranked by prominence (size, centering, confidence). When the camera's active AF points land on
    someone, that person is who the photographer meant, so they lead even if smaller or softer than a bystander.
    """
    acfg = cfg.get("af") or {}
    for p in people:
        p["af_score"] = A.person_score(af, p) if af else None
    people.sort(key=lambda p: -p["priority"])
    if not people or not af or not acfg.get("use", True):
        return "priority"
    best = max(people, key=lambda p: (p["af_score"] or 0, p["priority"]))
    if (best["af_score"] or 0) < acfg.get("min_score", 0.5):
        return "priority"
    people.remove(best)
    people.insert(0, best)
    return "af"


def _primary_fields(primary: Optional[dict]) -> dict:
    def rnd(v):
        return None if v is None else round(v, 4)
    return {
        "primary_head_sharp": rnd(primary["sharp_head"]) if primary else None,
        "primary_body_sharp": rnd(primary["sharp_body"]) if primary else None,
        "primary_eye_sharp": rnd(primary.get("sharp_eye")) if primary else None,
        "primary_eye_hf": rnd(primary.get("hf_eye")) if primary else None,
        "primary_eye_src": primary.get("eye_src") if primary else None,
    }


def analyze(path: Path, cfg: dict, detector: Detector, faces: Optional[FaceLandmarks] = None) -> LocalResult:
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
        })
        # Keep each metric's terms (Laplacian and gray variance, measured size) next to the ratio itself.
        r["terms"] = {}
        for name, key in (("head", "head"), ("torso", "torso"), ("body", "box")):
            t = sharpness_parts(_region(gray, r[key]))
            r[f"sharp_{name}"] = t["lap_var"] / (t["gray_var"] + EPS) if t else None
            r["terms"][name] = _sig(t)
        r["priority"] = round(area_frac * (1 - 0.5 * center_dist) * (0.5 + 0.5 * r["conf"]), 5)
        r["_det"] = d
        people.append(r)
        mask[bx[1]:bx[3], bx[0]:bx[2]] = False

    # Background sharpness: whole frame downscaled, persons masked out.
    s = 1024 / max(W, H)
    g_small = cv2.resize(gray, (max(1, round(W * s)), max(1, round(H * s))), interpolation=cv2.INTER_AREA)
    m_small = cv2.resize(mask.astype(np.uint8), g_small.shape[::-1], interpolation=cv2.INTER_NEAREST).astype(bool)
    gb = cv2.GaussianBlur(g_small, (0, 0), 1.0)
    lap = cv2.Laplacian(gb, cv2.CV_32F, ksize=3)
    bg_terms = {"lap_var": float(lap[m_small].var()), "gray_var": float(gb[m_small].var()), "px_count": int(m_small.sum())} \
        if m_small.sum() > 5000 else None
    bg_sharp = bg_terms["lap_var"] / (bg_terms["gray_var"] + EPS) if bg_terms else None
    global_terms = {"lap_var": float(lap.var()), "gray_var": float(gb.var()), "px": [g_small.shape[1], g_small.shape[0]]}
    global_sharp = global_terms["lap_var"] / (global_terms["gray_var"] + EPS)

    af, af_note = A.read_with_note(path, W, H, (cfg.get("af") or {}).get("y_up", True))
    primary_by = pick_primary(people, af, cfg)
    # Eye bands for the most prominent people only (a crowd shot can have dozens of tiny faces).
    rgb = np.asarray(im) if people else None
    for i, p in enumerate(people):
        det = p.pop("_det")
        if i < cfg.get("eye_max_people", 4):
            _eye_metrics(p, det, scale, rgb, gray, W, H, faces)
    primary = people[0] if people else None
    exif = X.read(path)
    prior = X.prior(exif, cfg.get("exif"))
    tier, reason = local_tier(primary, people[1:], cfg["focus"], prior, cfg.get("exif", {}).get("shake_margin", 1.5))

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
        "people": [{k: (rnd(v) if k.startswith(("sharp", "hf_")) else v) for k, v in p.items()} for p in people[:6]],
        "mask_boxes": [p["box"] for p in people],  # every person, masked out of the background metric
        "bg_sharp": rnd(bg_sharp), "global_sharp": rnd(global_sharp),
        "bg_terms": _sig(bg_terms), "global_terms": _sig(global_terms), "eps": EPS,
        **_primary_fields(primary),
        "af": af, "af_note": af_note, "primary_by": primary_by,
        "crop_box": crop_used,
        "exif": exif, "exif_prior": prior,
        "local_tier": tier, "local_reason": reason,
    }
    return LocalResult(data, frame, crop_jpeg)


def rescore(db, cfg: dict, backfill_exif: bool = True) -> dict:
    """Re-derive local tiers from stored metrics with the current thresholds (no re-detection).

    Rows analyzed before the EXIF prior or AF points existed get them read from the file (header only, fast),
    and the primary person is re-picked from the AF points.
    """
    changed = backfilled = af_new = reordered = 0
    for r in db.rows("local_json IS NOT NULL"):
        d = json.loads(r["local_json"])
        dirty = False
        if backfill_exif and "exif" not in d:
            d["exif"] = X.read(Path(r["path"]))
            backfilled += 1
            dirty = True
        if "exif" in d:
            d["exif_prior"] = X.prior(d["exif"], cfg.get("exif"))
        people = d.get("people") or []
        if backfill_exif and d.get("af") is None and d.get("width"):
            # Also retries rows whose earlier read came back empty (the first reader missed Canon 0x0026).
            af, note = A.read_with_note(Path(r["path"]), d["width"], d["height"], (cfg.get("af") or {}).get("y_up", True))
            if af is not None or note != d.get("af_note") or "af" not in d:
                d["af"], d["af_note"] = af, note
                af_new += af is not None
                dirty = True
        old_first, old_by = (people[0].get("box") if people else None), d.get("primary_by")
        d["primary_by"] = pick_primary(people, d.get("af"), cfg)
        dirty |= d["primary_by"] != old_by
        if people and people[0].get("box") != old_first:
            # Stored people carry their own metrics, so the new primary's numbers are already here; only the
            # head crop stays the old one until the next local pass.
            d.update(_primary_fields(people[0]))
            reordered += 1
            dirty = True
        tier, reason = local_tier(people[0] if people else None, people[1:], cfg["focus"], d.get("exif_prior"),
                                  cfg.get("exif", {}).get("shake_margin", 1.5))
        if tier != d.get("local_tier") or dirty:
            if tier != d.get("local_tier"):
                changed += 1
            d["local_tier"], d["local_reason"] = tier, reason
            db.set_local(r["id"], d)
    return {"changed": changed, "exif_backfilled": backfilled, "af_backfilled": af_new, "primary_changed": reordered}


THUMB_LONG_EDGE = 400


def write_cache(cache_dir: Path, img_id: int, res: "LocalResult"):
    (cache_dir / f"{img_id}.jpg").write_bytes(res.frame_jpeg)
    cp = cache_dir / f"{img_id}_crop.jpg"
    if res.crop_jpeg:
        cp.write_bytes(res.crop_jpeg)
    elif cp.exists():
        cp.unlink()
    (cache_dir / f"{img_id}_full.jpg").unlink(missing_ok=True)  # the viewer re-renders it from the file on demand
    import io
    thumb = I.resize_long_edge(Image.open(io.BytesIO(res.frame_jpeg)), THUMB_LONG_EDGE)
    (cache_dir / f"{img_id}_thumb.jpg").write_bytes(I.to_jpeg(thumb, 80))


def run_local(db, cfg: dict, cache_dir: Path, ids_paths: list[tuple[int, str]], device: Optional[str] = None,
              progress=None, should_stop=None, detector: Optional[Detector] = None):
    """Analyze images concurrently and store results. progress.update(1) per image; should_stop() aborts.
    A progress object with start(id, path) / finish(id, error) also hears when each image begins and ends."""
    cache_dir.mkdir(parents=True, exist_ok=True)
    det = detector or Detector(cfg["detect_model"], cfg["detect_long_edge"], cfg["detect_conf"], device)
    det(Image.new("RGB", (64, 64)))  # warm up / download weights before threads start
    faces = None
    if cfg["focus"].get("use_eyes", True):
        faces = FaceLandmarks(cfg.get("face_model", FACE_WEIGHTS), cfg.get("face_conf", 0.6))
        try:
            faces._net()  # fetch the weights once, before the threads race for them
        except Exception as e:  # no weights and no egress: fall back to pose eye keypoints
            import logging
            logging.getLogger(__name__).warning("face landmarks unavailable (%s); using pose eye keypoints", e)
            faces = None

    def work(img_id, path):
        if should_stop and should_stop():
            return img_id, None
        if hasattr(progress, "start"):
            progress.start(img_id, path)
        res = analyze(Path(path), cfg, det, faces)
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
                err = None
            except Exception as e:  # keep going; record the failure
                err = f"local: {type(e).__name__}: {e}"
                db.set_local(i, None, err)
                n_err += 1
            if hasattr(progress, "finish"):
                progress.finish(i, err)
            if progress:
                progress.update(1)
    return n_ok, n_err
