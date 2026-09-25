"""Visual intermediates of the local focus math, for the UI's image detail view.

Recomputes, from the original file, what photosort.local measures: the eye band at native resolution, the
Laplacian response the sharpness metric takes the variance of, the eye band's power spectrum with the radial
energy profile the FFT ratio integrates, and a frame-wide tile map of the same Laplacian metric. Nothing here
feeds a tier; it only shows the work.
"""
from __future__ import annotations
import base64
from pathlib import Path
from typing import Optional

import cv2
import numpy as np

from . import images as I
from .local import EYE_MIN_PX, MIN_REGION_PX, hf_ratio

HEAT_TILES = 96          # tiles along the frame's long edge
PROFILE_BINS = 60        # radial spectrum bins from 0 to 1.0 x Nyquist


def _png(a: np.ndarray) -> str:
    ok, buf = cv2.imencode(".png", a)
    return "data:image/png;base64," + base64.b64encode(buf.tobytes()).decode()


def _jpg(a: np.ndarray, q: int = 90) -> str:
    ok, buf = cv2.imencode(".jpg", a, [cv2.IMWRITE_JPEG_QUALITY, q])
    return "data:image/jpeg;base64," + base64.b64encode(buf.tobytes()).decode()


def _prep(gray: np.ndarray) -> np.ndarray:
    """The same downscale local.sharpness / hf_ratio apply to regions larger than 512 px."""
    h, w = gray.shape[:2]
    if max(h, w) > 512:
        s = 512 / max(h, w)
        gray = cv2.resize(gray, (max(1, round(w * s)), max(1, round(h * s))), interpolation=cv2.INTER_AREA)
    return gray


def laplacian_view(gray: np.ndarray, min_px: int) -> Optional[dict]:
    """|Laplacian| of the lightly blurred region, colormapped, plus the two variances the metric divides."""
    if gray is None or min(gray.shape[:2]) < min_px:
        return None
    g = cv2.GaussianBlur(_prep(gray), (0, 0), 1.0)
    lap = cv2.Laplacian(g, cv2.CV_32F, ksize=3)
    mag = np.abs(lap)
    top = float(np.percentile(mag, 99.5)) or 1e-6
    img = cv2.applyColorMap(np.clip(mag / top * 255, 0, 255).astype(np.uint8), cv2.COLORMAP_INFERNO)
    lap_var, g_var = float(lap.var()), float(g.var())
    return {"img": _jpg(img), "lap_var": lap_var, "gray_var": g_var, "eps": 2e-3,
            "value": lap_var / (g_var + 2e-3), "size": [g.shape[1], g.shape[0]]}


def spectrum_view(gray: np.ndarray, band=(0.25, 0.75), floor: float = 0.03) -> Optional[dict]:
    """Log power spectrum (DC centered) of the Hann-windowed region and the radial energy profile."""
    if gray is None or min(gray.shape[:2]) < EYE_MIN_PX:
        return None
    gray = _prep(gray)
    h, w = gray.shape[:2]
    win = (gray - gray.mean()) * np.outer(np.hanning(h), np.hanning(w)).astype(np.float32)
    p = np.abs(np.fft.fft2(win)) ** 2
    r = np.hypot(np.fft.fftfreq(h)[:, None], np.fft.fftfreq(w)[None, :]) / 0.5
    logp = np.log10(np.fft.fftshift(p) + 1e-12)
    lo, hi = np.percentile(logp, 5), logp.max()
    img = cv2.applyColorMap(np.clip((logp - lo) / (hi - lo + 1e-9) * 255, 0, 255).astype(np.uint8), cv2.COLORMAP_VIRIDIS)
    # Radial profile over the same (full, not rfft) spectrum: share of the floor..band[1] energy per bin.
    edges = np.linspace(0, 1.0, PROFILE_BINS + 1)
    idx = np.digitize(r.ravel(), edges) - 1
    ok = (idx >= 0) & (idx < PROFILE_BINS)
    energy = np.bincount(idx[ok], weights=p.ravel()[ok], minlength=PROFILE_BINS)
    counted = float(p[(r >= floor) & (r <= band[1])].sum())
    # The ratio's own sums, over the half spectrum exactly as local.hf_ratio takes them.
    ph = np.abs(np.fft.rfft2(win)) ** 2
    rh = np.hypot(np.fft.fftfreq(h)[:, None], np.fft.rfftfreq(w)[None, :]) / 0.5
    total = float(ph[(rh >= floor) & (rh <= band[1])].sum())
    in_band = float(ph[(rh >= band[0]) & (rh <= band[1])].sum())
    return {"img": _jpg(img), "size": [w, h], "band": list(band), "floor": floor,
            "profile": {"edges": edges.round(4).tolist(), "energy": (energy / (counted or 1)).tolist()},
            "band_energy": in_band, "total_energy": total, "value": hf_ratio(gray, band, floor)}


def heatmap(gray: np.ndarray) -> dict:
    """Contrast-normalized Laplacian variance per tile over the whole frame at native resolution."""
    H, W = gray.shape
    t = max(16, int(np.ceil(max(W, H) / HEAT_TILES)))
    g = cv2.GaussianBlur(gray, (0, 0), 1.0)
    lap = cv2.Laplacian(g, cv2.CV_32F, ksize=3)
    ny, nx = H // t, W // t
    tiles = lambda a: a[: ny * t, : nx * t].reshape(ny, t, nx, t).swapaxes(1, 2).reshape(ny, nx, t * t)
    v = tiles(lap).var(axis=2) / (tiles(g).var(axis=2) + 2e-3)
    lv = np.log10(v + 1e-6)
    lo, hi = float(np.percentile(lv, 2)), float(np.percentile(lv, 99.5))
    u = np.clip((lv - lo) / (hi - lo + 1e-9), 0, 1)
    rgb = cv2.applyColorMap((u * 255).astype(np.uint8), cv2.COLORMAP_TURBO)
    rgba = np.dstack([rgb, (20 + 200 * u ** 1.5).astype(np.uint8)])  # soft tiles fade out, sharp ones stand out
    return {"img": _png(rgba), "tile": t, "grid": [nx, ny], "cover": [nx * t, ny * t],
            "log_range": [round(lo, 3), round(hi, 3)]}


def focus_debug(path: Path, local: dict) -> dict:
    im = I.load_rgb(path)
    gray = np.asarray(im.convert("L"), dtype=np.float32) / 255.0
    W, H = im.size
    people = []
    for p in local.get("people") or []:
        out: dict = {}
        if p.get("eye"):
            x0, y0, x1, y1 = p["eye"]
            region = gray[y0:y1, x0:x1]
            out["eye"] = {"box": p["eye"], "img": _jpg(np.asarray(im.crop((x0, y0, x1, y1)))[:, :, ::-1], 95),
                          "laplacian": laplacian_view(region, EYE_MIN_PX), "spectrum": spectrum_view(region)}
        if p.get("head"):
            x0, y0, x1, y1 = p["head"]
            region = gray[y0:y1, x0:x1]
            if min(region.shape[:2]) >= MIN_REGION_PX:
                out["head"] = {"box": p["head"], "img": _jpg(np.asarray(im.crop((x0, y0, x1, y1)).resize(
                    _prep(region).shape[::-1]))[:, :, ::-1]), "laplacian": laplacian_view(region, MIN_REGION_PX)}
        people.append(out)
    return {"width": W, "height": H, "heatmap": heatmap(gray), "people": people}
