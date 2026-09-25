"""Camera metadata as a static prior on focus and motion blur.

A wide-open lens (f/1.4) makes a missed focus plane unsurprising; a slow shutter
(1/30 s) makes motion blur likely. Neither replaces measuring the pixels, but both
tell the pipeline how much to trust a borderline sharpness score and tell the vision
model what to expect.
"""
from __future__ import annotations
import math
from pathlib import Path
from typing import Optional

FIELDS = ("camera", "lens", "f_number", "shutter_s", "iso", "focal_mm", "focal_35mm", "taken")

_TAGS = {
    "camera": ("Image Model",),
    "lens": ("EXIF LensModel", "MakerNote LensModel", "MakerNote LensType"),
    "f_number": ("EXIF FNumber",),
    "shutter_s": ("EXIF ExposureTime",),
    "iso": ("EXIF ISOSpeedRatings", "EXIF PhotographicSensitivity"),
    "focal_mm": ("EXIF FocalLength",),
    "focal_35mm": ("EXIF FocalLengthIn35mmFilm",),
    "taken": ("EXIF DateTimeOriginal", "Image DateTime"),
}
_NUMERIC = {"f_number", "shutter_s", "iso", "focal_mm", "focal_35mm"}


def _num(tag) -> Optional[float]:
    v = getattr(tag, "values", None)
    while isinstance(v, (list, tuple)):
        if not v:
            return None
        v = v[0]
    try:
        if hasattr(v, "num") and hasattr(v, "den"):
            return float(v.num) / float(v.den) if v.den else None
        return float(v)
    except (TypeError, ValueError):
        try:
            return float(str(tag))
        except ValueError:
            return None


def read(path: Path) -> dict:
    """Best-effort EXIF read for JPEG/TIFF/HEIC and TIFF-based raws (CR2, NEF, ARW, DNG...).

    Returns {} when the file carries no usable metadata. Never raises.
    """
    out: dict = {}
    try:
        import exifread
        with open(path, "rb") as f:
            tags = exifread.process_file(f, details=False)
    except Exception:
        tags = {}
    if not tags:
        tags = _pillow_tags(path)
    for key, names in _TAGS.items():
        for name in names:
            tag = tags.get(name)
            if tag is None:
                continue
            val = _num(tag) if key in _NUMERIC else str(tag).strip()
            if val not in (None, "", 0):
                out[key] = round(val, 6) if isinstance(val, float) else val
                break
    return out


def _pillow_tags(path: Path) -> dict:
    """Fallback for formats exifread can't parse; mimics exifread's tag naming."""
    try:
        from PIL import Image
        from PIL.ExifTags import Base as T
        ex = Image.open(path).getexif()
        ifd = ex.get_ifd(0x8769)  # Exif sub-IFD
    except Exception:
        return {}

    class _V:
        def __init__(self, v): self.values = [v]
        def __str__(self): return str(self.values[0])

    names = [("Image Model", ex, T.Model), ("Image DateTime", ex, T.DateTime),
             ("EXIF FNumber", ifd, T.FNumber), ("EXIF ExposureTime", ifd, T.ExposureTime),
             ("EXIF ISOSpeedRatings", ifd, T.ISOSpeedRatings), ("EXIF FocalLength", ifd, T.FocalLength),
             ("EXIF FocalLengthIn35mmFilm", ifd, T.FocalLengthIn35mmFilm), ("EXIF LensModel", ifd, T.LensModel),
             ("EXIF DateTimeOriginal", ifd, T.DateTimeOriginal)]
    return {n: _V(src[t]) for n, src, t in names if t in src}


def fmt_shutter(s: Optional[float]) -> str:
    if not s:
        return "?"
    return f"1/{round(1 / s)} s" if s < 0.5 else f"{s:g} s"


def prior(exif: dict, cfg: Optional[dict] = None) -> dict:
    """Derive depth-of-field and motion-blur risk from the exposure settings.

    shake_stops: log2(shutter * focal35) — 0 means exactly the 1/focal-length rule,
    +1 means one stop slower than that (handheld shake likely), -3 means 3 stops faster.
    Subject motion (riders) needs faster still; `action_shutter` marks that bar.
    """
    cfg = cfg or {}
    crop = float(cfg.get("crop_factor", 1.0))
    wide = float(cfg.get("wide_open_f", 2.0))
    action_s = float(cfg.get("action_shutter", 1 / 500))
    f, s = exif.get("f_number"), exif.get("shutter_s")
    focal35 = exif.get("focal_35mm") or (exif["focal_mm"] * crop if exif.get("focal_mm") else None)
    out: dict = {"dof_risk": None, "motion_risk": None, "shake_stops": None, "pupil_mm": None, "summary": None}
    if not exif:
        return out
    if f:
        # Entrance pupil (focal / f-number) tracks background blur and focus-plane thinness far better than
        # the f-number alone: 135mm f/2.2 (61mm pupil) is as unforgiving as 85mm f/1.4 (61mm).
        pupil = focal35 / f if focal35 else None
        out["pupil_mm"] = round(pupil, 1) if pupil else None
        if f <= wide or (pupil and pupil >= 40):
            out["dof_risk"] = "high"
        elif f <= 4.0 or (pupil and pupil >= 15):
            out["dof_risk"] = "medium"
        else:
            out["dof_risk"] = "low"
    if s:
        if focal35:
            out["shake_stops"] = round(math.log2(s * focal35), 2)
        shake = out["shake_stops"] if out["shake_stops"] is not None else -99
        if shake >= 1.0 or s >= 1 / 60:
            out["motion_risk"] = "high"
        elif shake >= -1.0 or s > action_s:
            out["motion_risk"] = "medium"
        else:
            out["motion_risk"] = "low"
    parts = [p for p in (exif.get("camera"), f"{focal35:g}mm" if focal35 else None, f"f/{f:g}" if f else None,
                         fmt_shutter(s) if s else None, f"ISO {exif['iso']:g}" if exif.get("iso") else None) if p]
    notes = []
    if out["dof_risk"] == "high":
        notes.append("very shallow depth of field: only the focus plane can be sharp, judge the head/eyes")
    elif out["dof_risk"] == "medium":
        notes.append("moderate depth of field")
    if out["motion_risk"] == "high":
        notes.append("slow shutter for this focal length: motion blur likely" if (out["shake_stops"] or 0) >= 1 else "slow shutter: motion blur likely")
    elif out["motion_risk"] == "medium":
        notes.append("shutter marginal for a moving subject")
    elif out["motion_risk"] == "low":
        notes.append("fast shutter: motion blur unlikely")
    out["summary"] = " · ".join(parts) + (". " + "; ".join(notes) + "." if notes else "")
    return out
