"""The focus metrics and the geometry around them: regions per person, eye bands, face landmarks, dedup.

Moved verbatim from photosort/local.py: the calibrated thresholds depend on these exact numerics (OpenCV
filters with their default borders, INTER_AREA, float32 FFT), so nothing here may change without a recalibration.
"""
from __future__ import annotations
import threading
from typing import Optional

import cv2
import numpy as np

# COCO keypoint indices used by YOLO pose models
NOSE, LEYE, REYE, LEAR, REAR, LSHO, RSHO, LHIP, RHIP = 0, 1, 2, 3, 4, 5, 6, 11, 12
HEAD_KP = [NOSE, LEYE, REYE, LEAR, REAR]
BODY_KP = [LSHO, RSHO, LHIP, RHIP]
MIN_REGION_PX = 40
EYE_MIN_PX = 24      # an eye band is small by nature; below this there is nothing to judge
FACE_WEIGHTS = "face_detection_yunet_2023mar.onnx"


EPS = 2e-3   # contrast floor in the Laplacian normalization: keeps flat regions from dividing by ~0


def _rnd(v):
    return None if v is None else round(v, 4)


def _ratio(t: Optional[dict]) -> Optional[float]:
    """The contrast-normalized Laplacian from its terms (see sharpness_parts)."""
    return None if t is None else t["lap_var"] / (t["gray_var"] + EPS)


def _fit512(gray: np.ndarray) -> np.ndarray:
    h, w = gray.shape[:2]
    if max(h, w) > 512:
        s = 512 / max(h, w)
        gray = cv2.resize(gray, (max(1, round(w * s)), max(1, round(h * s))), interpolation=cv2.INTER_AREA)
    return gray


def _laplacian(gray: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """The lightly blurred region and its Laplacian: the blur keeps sensor noise out of the metric."""
    g = cv2.GaussianBlur(gray, (0, 0), 1.0)
    return g, cv2.Laplacian(g, cv2.CV_32F, ksize=3)


def sharpness_parts(gray: np.ndarray, min_px: int = MIN_REGION_PX) -> Optional[dict]:
    """The terms of the sharpness metric: {"lap_var", "gray_var", "px": [w, h] measured at}, or None."""
    if gray is None or min(gray.shape[:2]) < min_px:
        return None
    g, lap = _laplacian(_fit512(gray))
    return {"lap_var": float(lap.var()), "gray_var": float(g.var()), "px": [g.shape[1], g.shape[0]]}


def sharpness(gray: np.ndarray, min_px: int = MIN_REGION_PX) -> Optional[float]:
    """Contrast-normalized Laplacian variance on a float32 grayscale array in [0,1].

    A light Gaussian blur first suppresses sensor noise (which otherwise inflates the
    metric at high ISO). Normalizing by the region's own variance makes a low-contrast
    but sharp region comparable to a high-contrast one. Higher = sharper.
    """
    return _ratio(sharpness_parts(gray, min_px))


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


NOISE_K = np.array([[1, -2, 1], [-2, 4, -2], [1, -2, 1]], np.float32)


def noise_sigma(gray: np.ndarray, flat_q: float = 50) -> Optional[float]:
    """Sensor noise sigma of a float32 grayscale image in [0,1], in 8-bit levels, or None when there's too little to read.

    Immerkær's fast estimate: the kernel is the difference of two Laplacians, which cancels edges and smooth
    gradients, so what it leaves on a flat patch is noise, and sigma = sqrt(pi/2) / 6 x mean |response|. Texture still
    leaks through, so only the flattest flat_q percent of pixels count (by gradient magnitude), and near-clipped
    pixels, where the noise is cut off, are left out. Filtered at native resolution (downscaling averages noise away);
    the statistics then read every other row and column, which is plenty and keeps a 20 MP frame near 0.1 s."""
    if gray is None or min(gray.shape[:2]) < 16:
        return None
    s = (slice(1, -1, 2), slice(1, -1, 2))   # the border rows and columns see the filters' padding
    r = cv2.filter2D(gray, cv2.CV_32F, NOISE_K)[s]
    b = cv2.GaussianBlur(gray, (0, 0), 1.0)
    grad = (np.abs(cv2.Sobel(b, cv2.CV_32F, 1, 0, ksize=3)) + np.abs(cv2.Sobel(b, cv2.CV_32F, 0, 1, ksize=3)))[s]
    g = gray[s]
    ok = (g > 0.02) & (g < 0.98)
    if ok.sum() < 250:
        return None
    ok &= grad <= np.percentile(grad[ok][::8], flat_q)   # a sample sets the cut as well as every pixel would
    return float(np.sqrt(np.pi / 2) / 6 * np.abs(r[ok]).mean() * 255) if ok.any() else None


PLANE_PRE = 1.0      # denoise blur before the edge-width estimate; subtracted back out in quadrature
PLANE_MIN_STEP = 0.12
PLANE_MIN_EDGES = 40


def _edge_maps(gray: np.ndarray):
    """Per-pixel inputs to the edge-width estimate: gradient magnitude, local step height, strong-edge mask."""
    b = cv2.GaussianBlur(gray, (0, 0), PLANE_PRE)
    mag = np.hypot(cv2.Sobel(b, cv2.CV_32F, 1, 0, ksize=3), cv2.Sobel(b, cv2.CV_32F, 0, 1, ksize=3)) / 8
    k = np.ones((11, 11), np.uint8)
    step = cv2.dilate(b, k) - cv2.erode(b, k)
    edges = (cv2.Canny((b * 255).astype(np.uint8), 40, 90, L2gradient=True) > 0) & (step >= PLANE_MIN_STEP)
    return mag, step, edges


def _edge_blur(mag, step, edges, q: float = 90, min_n: int = PLANE_MIN_EDGES) -> Optional[float]:
    """Blur width in native pixels from the steepest strong edges, or None with too few of them.

    A step blurred by a Gaussian of width s rises with a peak slope of step / (s·√(2π)), so slope ÷ step
    measures s whatever the edge's contrast or what it belongs to (grass, jersey, helmet). Unlike the
    Laplacian ratio, that makes it comparable across different content."""
    if edges.sum() < min_n:
        return None
    s = 1 / (float(np.percentile(mag[edges] / step[edges], q)) * np.sqrt(2 * np.pi))
    return float(np.sqrt(max(s * s - PLANE_PRE ** 2, 0.01)))


def _grow(b, fx: float, fy: float, W: int, H: int):
    w, h = b[2] - b[0], b[3] - b[1]
    return _clamp_box((b[0] - fx * w, b[1] - fy * h, b[2] + fx * w, b[3] + fy * h), W, H)


def _extra(a: Optional[float], b: Optional[float]) -> Optional[float]:
    """How much more blur a carries than b, in px (blur widths add in quadrature); None if either is missing."""
    return None if a is None or b is None else round(float(np.sqrt(max(a * a - b * b, 0))), 3)


def focus_plane(gray: np.ndarray, p: dict, W: int, H: int) -> Optional[dict]:
    """Is the focus plane on this person's head, or next to it? Edge-width blur (see _edge_blur) of the head,
    the torso and the surroundings, each measured on its own: at shallow depth of field the torso can sit in
    another plane, so a sharp jersey must not vouch for a soft face. The surroundings are everything within
    one person-size, outside a margin that takes in the helmets, hair and limbs the boxes miss.

    A head in focus is the sharpest thing at its depth, so surroundings that are clearly sharper mean focus
    landed in front or behind. Returns blur σ in px for "head", "torso" and "near", the extra blur the head
    carries over each ("head_vs_near", "head_vs_torso") and the torso over the surroundings ("torso_vs_near"),
    and the edge counts; None without a head reading. "near" is None when the surroundings have too few edges
    (bokeh). Clothing print is steeper than any face, so head_vs_torso reads high on sharp frames too.
    """
    head, torso, box = p["head"], p["torso"], p["box"]
    if min(head[2] - head[0], head[3] - head[1]) < MIN_REGION_PX:
        return None
    m = max(box[2] - box[0], box[3] - box[1])
    X0, Y0, X1, Y1 = _clamp_box((box[0] - m, min(box[1], head[1]) - m, box[2] + m, box[3] + m), W, H)
    mag, step, edges = _edge_maps(gray[Y0:Y1, X0:X1])
    crop = lambda b: tuple(a[b[1] - Y0:b[3] - Y0, b[0] - X0:b[2] - X0] for a in (mag, step, edges))
    # Each region pools its edge pixels and takes the same statistic, so a region with many more edges (the
    # surroundings) doesn't win by picking its luckiest tile. The torso and the surroundings each need a
    # decent sample; a sharp strip at least a tenth of their edges wide is enough to show.
    s_head = _edge_blur(*crop(head))
    if s_head is None:
        return None
    yy, xx = np.mgrid[Y0:Y1, X0:X1]
    inside = lambda b: (xx >= b[0]) & (xx < b[2]) & (yy >= b[1]) & (yy < b[3])
    zone = inside(_grow(box, 0.15, 0.1, W, H)) | inside(_grow(head, 0.6, 1.0, W, H))
    tor_m, near_m = inside(torso) & ~inside(head), ~zone
    s_tor = _edge_blur(mag, step, edges & tor_m, min_n=4 * PLANE_MIN_EDGES)
    s_near = _edge_blur(mag, step, edges & near_m, min_n=4 * PLANE_MIN_EDGES)
    n_tor, n_near = int((edges & tor_m).sum()), int((edges & near_m).sum())
    r3 = lambda v: None if v is None else round(v, 3)
    return {"head": r3(s_head), "torso": r3(s_tor), "near": r3(s_near),
            "head_vs_near": _extra(s_head, s_near), "head_vs_torso": _extra(s_head, s_tor),
            "torso_vs_near": _extra(s_tor, s_near), "n_torso": n_tor, "n_near": n_near}


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


class FaceLandmarks:
    """OpenCV YuNet face detector (box + eyes/nose/mouth corners), run on a native-resolution crop around
    the pose head box. One net per thread (cv2.dnn nets are not safe to share); loads lazily."""

    def __init__(self, weights: str = FACE_WEIGHTS, conf: float = 0.6):
        self.weights, self.conf = weights, conf
        self._tls = threading.local()

    def _net(self):
        if getattr(self._tls, "net", None) is None:
            from .weights import weights_path
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


def _iou(a, b) -> float:
    iw = max(0.0, min(a[2], b[2]) - max(a[0], b[0]))
    ih = max(0.0, min(a[3], b[3]) - max(a[1], b[1]))
    inter = iw * ih
    union = (a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter
    return inter / union if union > 0 else 0.0


def _same_spot(a: dict, b: dict, idx: list[int], tol: float, min_conf: float, min_n: int) -> Optional[bool]:
    """Whether both detections put the keypoints in idx (those both see at min_conf) in the same place, within
    tol * sqrt of the smaller box's area on average. None when fewer than min_n are shared: nothing to compare."""
    shared = [i for i in idx if a["kpc"][i] >= min_conf and b["kpc"][i] >= min_conf]
    if len(shared) < min_n:
        return None
    size = min(np.sqrt((d["box"][2] - d["box"][0]) * (d["box"][3] - d["box"][1])) for d in (a, b))
    dist = np.mean([np.hypot(a["kp"][i][0] - b["kp"][i][0], a["kp"][i][1] - b["kp"][i][1]) for i in shared])
    return bool(dist <= tol * size)


def _same_person(a: dict, b: dict, tol: float) -> bool:
    """Both detections are one person: their confident head keypoints sit in the same spot, or, when one of
    them doesn't see the head (a box cut off at the shoulders), their shoulders and hips do."""
    if not (a.get("kp") and a.get("kpc") and b.get("kp") and b.get("kpc")):
        return False
    same = _same_spot(a, b, HEAD_KP, tol, 0.3, 1)
    if same is None:
        same = _same_spot(a, b, BODY_KP, tol, 0.5, 2)
    return bool(same)


def _sees_head(d: dict) -> bool:
    return bool(d.get("kpc")) and any(d["kpc"][i] >= 0.5 for i in HEAD_KP)


def dedup_detections(dets: list[dict], cfg: dict) -> list[dict]:
    """Drop duplicate detections of one person that slip past YOLO's NMS (its IoU cut is 0.7; a second,
    shifted box with a hallucinated limb often lands at 0.5-0.7). A box is a duplicate when it overlaps a kept
    one by dedup_iou, or by dedup_head_iou with the heads (or, if one misses the head, the torsos) in the same
    spot. The keypoint test keeps two real people apart even when one stands in front of the other.

    Boxes that see the head are kept first: a headless duplicate (one box from the shoulders down, another
    over the whole rider) often scores higher, but the head is what focus is judged on."""
    d_iou, h_iou, h_tol = cfg.get("dedup_iou", 0.6), cfg.get("dedup_head_iou", 0.25), cfg.get("dedup_head_tol", 0.1)
    kept: list[dict] = []
    for d in sorted(dets, key=lambda d: (not _sees_head(d), -d["conf"])):
        if not any((o := _iou(d["box"], k["box"])) >= d_iou or (o >= h_iou and _same_person(d, k, h_tol)) for k in kept):
            kept.append(d)
    return kept


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

