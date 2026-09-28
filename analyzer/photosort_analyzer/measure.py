"""One image through the pixel stage, in two calls around the backend's decision.

measure()   decode (with exposure lift), detect and dedup people, per-person regions and head/torso/body sharpness,
            prominence, background/global sharpness, noise; writes the frame and thumbnail JPEGs. The decoded image
            is kept under a token for finalize().
finalize()  given the order the backend picked (primary first, from prominence and the camera's AF points): eye
            bands for the first eye_max_people, the focus plane of the primary, and the primary's crop JPEG.

The split keeps every pixel operation here and every decision in Go, while doing exactly the work analyze() used to.

With a cache_dir (the sidecar, the CLI) the JPEGs are written there. Without one (a pooled analyzer that shares no disk
with the backend) they come back in the answer instead, under "files": name -> base64, or None for a file the backend
should remove.
"""
from __future__ import annotations
import base64
import logging
import threading
import time
import uuid
from collections import OrderedDict
from pathlib import Path
from typing import Optional

import cv2
import numpy as np

from . import detectors
from . import images as I
from . import metrics as M

log = logging.getLogger(__name__)
THUMB_LONG_EDGE = 400
SESSION_TTL_S = 300
SESSION_MAX = 32


class Gone(KeyError):
    """The token expired or was never issued (analyzer restarted): measure again."""


class _Sessions:
    def __init__(self):
        self._d: OrderedDict[str, tuple[float, dict]] = OrderedDict()
        self._lock = threading.Lock()

    def put(self, state: dict) -> str:
        tok = uuid.uuid4().hex
        now = time.monotonic()
        with self._lock:
            self._d[tok] = (now, state)
            while self._d and (len(self._d) > SESSION_MAX or next(iter(self._d.values()))[0] < now - SESSION_TTL_S):
                self._d.popitem(last=False)
        return tok

    def pop(self, tok: str) -> dict:
        with self._lock:
            got = self._d.pop(tok, None)
        if got is None:
            raise Gone(tok)
        return got[1]


sessions = _Sessions()
_faces: dict[tuple, Optional[M.FaceLandmarks]] = {}
_faces_lock = threading.Lock()


def faces_for(model: str, conf: float) -> Optional[M.FaceLandmarks]:
    """The face landmark model, or None when its weights can't be had (no copy and no egress): the eye bands then
    come from the pose eye keypoints alone."""
    with _faces_lock:
        if (model, conf) not in _faces:
            f = M.FaceLandmarks(model, conf)
            try:
                f._net()  # fetch the weights once, before threads race for them
            except Exception as e:
                log.warning("face landmarks unavailable (%s); using pose eye keypoints", e)
                f = None
            _faces[(model, conf)] = f
        return _faces[(model, conf)]


def _put(files: dict, cache_dir: Optional[Path], name: str, data: Optional[bytes]):
    """Write (or, for None, remove) one cached JPEG, or queue it in files for the backend to."""
    if cache_dir is None:
        files[name] = base64.b64encode(data).decode("ascii") if data is not None else None
    elif data is None:
        (cache_dir / name).unlink(missing_ok=True)
    else:
        (cache_dir / name).write_bytes(data)


def measure(req: dict, detector=None) -> dict:
    path = Path(req["path"])
    cache_dir = Path(req["cache_dir"]) if req.get("cache_dir") else None
    img_id = req["id"]
    d = req.get("detect") or {}
    im, exposure = I.load(path, req.get("exposure"))
    W, H = im.size
    small = I.resize_long_edge(im, req["detect_long_edge"])
    scale = W / small.size[0]
    det = detector or detectors.get(d.get("model", "yolo11n-pose.pt"), d.get("imgsz", req["detect_long_edge"]),
                                    d.get("conf", 0.25), d.get("iou", 0.7), d.get("device"))
    dets = [x for x in M.dedup_detections(det(small), req.get("dedup") or {})
            if ((x["box"][2] - x["box"][0]) * (x["box"][3] - x["box"][1]) * scale * scale) / (W * H) >= req["min_person_frac"]]

    gray = np.asarray(im.convert("L"), dtype=np.float32) / 255.0
    people = []
    mask = np.ones(gray.shape, dtype=bool)
    for x in dets:
        r = M._person_regions(x, scale, W, H)
        bx = r["box"]
        area_frac = ((bx[2] - bx[0]) * (bx[3] - bx[1])) / (W * H)
        cx, cy = (bx[0] + bx[2]) / 2 / W, (bx[1] + bx[3]) / 2 / H
        center_dist = float(np.hypot(cx - 0.5, cy - 0.5) / 0.7071)
        r.update({"area_frac": area_frac, "center": [round(cx, 3), round(cy, 3)], "center_dist": round(center_dist, 3)})
        r["terms"] = {}
        for name, key in (("head", "head"), ("torso", "torso"), ("body", "box")):
            t = M.sharpness_parts(M._region(gray, r[key]))
            r[f"sharp_{name}"] = M._ratio(t)
            r["terms"][name] = M._sig(t)
        r["priority"] = round(area_frac * (1 - 0.5 * center_dist) * (0.5 + 0.5 * r["conf"]), 5)
        people.append(r)
        mask[bx[1]:bx[3], bx[0]:bx[2]] = False

    # Background sharpness: whole frame downscaled, persons masked out.
    s = 1024 / max(W, H)
    g_small = cv2.resize(gray, (max(1, round(W * s)), max(1, round(H * s))), interpolation=cv2.INTER_AREA)
    m_small = cv2.resize(mask.astype(np.uint8), g_small.shape[::-1], interpolation=cv2.INTER_NEAREST).astype(bool)
    gb, lap = M._laplacian(g_small)
    bg_terms = {"lap_var": float(lap[m_small].var()), "gray_var": float(gb[m_small].var()), "px_count": int(m_small.sum())} \
        if m_small.sum() > 5000 else None
    global_terms = {"lap_var": float(lap.var()), "gray_var": float(gb.var()), "px": [g_small.shape[1], g_small.shape[0]]}

    fr = req.get("frame") or {}
    frame_im = I.resize_long_edge(im, fr.get("long_edge", 1568))
    files: dict = {}
    if cache_dir is not None:
        cache_dir.mkdir(parents=True, exist_ok=True)
    _put(files, cache_dir, f"{img_id}.jpg", I.to_jpeg(frame_im, fr.get("quality", 82)))
    _put(files, cache_dir, f"{img_id}_thumb.jpg", I.to_jpeg(I.resize_long_edge(frame_im, THUMB_LONG_EDGE), 80))

    token = sessions.put({"im": im, "gray": gray, "dets": dets, "people": people, "scale": scale, "W": W, "H": H,
                          "id": img_id, "cache_dir": cache_dir})
    out = {
        "token": token, "width": W, "height": H, "exposure": exposure, "detector": getattr(det, "name", None),
        "people": people,
        "bg_sharp": M._rnd(M._ratio(bg_terms)), "global_sharp": M._rnd(M._ratio(global_terms)),
        "bg_terms": M._sig(bg_terms), "global_terms": M._sig(global_terms), "eps": M.EPS,
        "noise_sigma": M._rnd(M.noise_sigma(gray)),
    }
    if cache_dir is None:
        out["files"] = files
    return out


def _eye_metrics(det: dict, head, scale: float, rgb, gray, W: int, H: int, faces: Optional[M.FaceLandmarks]) -> dict:
    """Locate the eyes (face landmarks first, then confident pose eye keypoints) and score the eye band."""
    found = faces.face(rgb, head, W, H) if faces is not None else None
    if found:
        e1, e2, src = found["eyes"][0], found["eyes"][1], "face"
    else:
        pe = M._pose_eyes(det, scale)
        e1, e2, src = (pe[0], pe[1], "pose") if pe else (None, None, None)
    band = M.eye_band(e1, e2, W, H) if e1 else None
    region = gray[band[1]:band[3], band[0]:band[2]] if band else None
    lt = M.sharpness_parts(region, M.EYE_MIN_PX) if band else None
    ht = M.hf_parts(region) if lt else None
    lap = M._ratio(lt)
    return {
        "eyes": [[round(e1[0]), round(e1[1])], [round(e2[0]), round(e2[1])]] if lap is not None else None,
        "eye_src": src if lap is not None else None, "eye": band if lap is not None else None,
        "sharp_eye": lap, "hf_eye": ht["band_e"] / ht["total_e"] if ht else None,
        "terms_eye": M._sig({**lt, **(ht or {})}) if lt else None,
        # What the face model saw, kept for the UI overlay even when the band ended up unusable.
        "face": None if not found else {
            "box": [round(v) for v in found["box"]], "search": list(found["search"]), "score": round(found["score"], 3),
            "lm": [[round(x), round(y)] for x, y in found["lm"]]},
    }


def finalize(req: dict) -> dict:
    st = sessions.pop(req["token"])
    people, dets, W, H = st["people"], st["dets"], st["W"], st["H"]
    order = req.get("order") or []
    if sorted(order) != list(range(len(people))):
        raise ValueError(f"order must be a permutation of 0..{len(people) - 1}")
    faces = faces_for(req.get("face_model", M.FACE_WEIGHTS), req.get("face_conf", 0.6)) if req.get("use_face", True) else None
    rgb = np.asarray(st["im"]) if people else None
    eyes = {}
    for idx in order[:req.get("eye_max_people", 4)]:
        eyes[idx] = _eye_metrics(dets[idx], people[idx]["head"], st["scale"], rgb, st["gray"], W, H, faces)
    primary = people[order[0]] if order else None
    plane = M.focus_plane(st["gray"], primary, W, H) if primary is not None and req.get("plane", True) else None

    cache_dir, img_id = st["cache_dir"], st["id"]
    crop_used, crop = None, None
    if primary is not None:
        c = req.get("crop") or {}
        crop_im, crop_used = I.crop_box(st["im"], primary["upper"], c.get("pad", 0.15), c.get("size", 768))
        crop = I.to_jpeg(crop_im, c.get("quality", 88))
    files: dict = {}
    _put(files, cache_dir, f"{img_id}_crop.jpg", crop)
    _put(files, cache_dir, f"{img_id}_full.jpg", None)  # the viewer re-renders it from the file on demand
    out = {"eyes": {str(k): v for k, v in eyes.items()}, "plane": plane, "crop_box": list(crop_used) if crop_used else None}
    if cache_dir is None:
        out["files"] = files
    return out


def detect(req: dict) -> dict:
    """Detections of one model on one image, in full-resolution pixels, stored nowhere: the UI's detector
    comparison."""
    d = req.get("detect") or {}
    im, _ = I.load(Path(req["path"]), req.get("exposure"))
    W, H = im.size
    small = I.resize_long_edge(im, req.get("detect_long_edge", 1280))
    scale = W / small.size[0]
    det = detectors.get(d["model"], d.get("imgsz", 1280), d.get("conf", 0.25), d.get("iou", 0.7), d.get("device"))
    t0 = time.perf_counter()
    dets = det(small)
    secs = time.perf_counter() - t0
    kept = M.dedup_detections(dets, req.get("dedup") or {})
    return {"width": W, "height": H, "model": d["model"], "seconds": round(secs, 3), "raw": len(dets),
            "people": [{"box": [round(v * scale, 1) for v in x["box"]], "conf": round(x["conf"], 4),
                        "kp": [[round(p[0] * scale, 1), round(p[1] * scale, 1), round(float(c), 3)]
                               for p, c in zip(x["kp"], x["kpc"])] if x.get("kp") and x.get("kpc") else None}
                       for x in kept]}
