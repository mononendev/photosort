from __future__ import annotations
import io
import os
from pathlib import Path
from typing import Iterable, Optional
import numpy as np
from PIL import Image, ImageOps

try:
    import pillow_heif
    pillow_heif.register_heif_opener()
except ImportError:  # pragma: no cover
    pass

RAW_EXT = {".arw", ".cr2", ".cr3", ".nef", ".nrw", ".dng", ".raf", ".orf", ".rw2", ".pef", ".srw", ".3fr", ".iiq"}
IMG_EXT = {".jpg", ".jpeg", ".png", ".heic", ".heif", ".tif", ".tiff", ".webp"}
ALL_EXT = RAW_EXT | IMG_EXT

Image.MAX_IMAGE_PIXELS = 400_000_000


def is_image(p: Path) -> bool:
    return p.suffix.lower() in ALL_EXT and not p.name.startswith(".")


def find_images(paths: Iterable[Path], skip_raw_dupes: bool = False) -> list[Path]:
    """Image files among `paths` and under the folders in it. With skip_raw_dupes, a RAW whose stem also has a
    JPEG/HEIC/... next to it is dropped (the other decodes faster). os.walk reads file types from the directory
    listing, so a network mount isn't stat'ed once per file."""
    files: list[Path] = []
    for p in paths:
        p = Path(p)
        if p.is_dir():
            for root, _, names in os.walk(p):
                files += [Path(root, n) for n in names if is_image(Path(n))]
        elif p.is_file() and is_image(p):
            files.append(p)
    if skip_raw_dupes:
        stems = {f.with_suffix("").as_posix() for f in files if f.suffix.lower() not in RAW_EXT}
        files = [f for f in files if f.suffix.lower() not in RAW_EXT or f.with_suffix("").as_posix() not in stems]
    return files


def _srgb_to_linear(v: np.ndarray) -> np.ndarray:
    return np.where(v <= 0.04045, v / 12.92, ((v + 0.055) / 1.055) ** 2.4).astype(np.float32)


def _linear_to_srgb(v: np.ndarray) -> np.ndarray:
    v = np.clip(v, 0, 1)
    return np.where(v <= 0.0031308, v * 12.92, 1.055 * np.power(v, 1 / 2.4) - 0.055).astype(np.float32)


_LUMA = np.array([0.2126, 0.7152, 0.0722], np.float32)


def exposure_stats(im: Image.Image) -> dict:
    """Scene brightness in linear light, from a small copy: the log-average ("key") and the 99th percentile."""
    small = np.asarray(resize_long_edge(im, 256).convert("RGB"), np.float32) / 255
    y = _srgb_to_linear(small) @ _LUMA
    return {"key": float(np.exp(np.log(y + 1e-4).mean())), "p99": float(np.percentile(y, 99))}


def plan_gain(stats: dict, ex: dict, raw: bool) -> float:
    """Stops of exposure to add, or 0. A frame is only lifted when its key sits under dark_key; the lift aims the key
    at target_key, stops short of pushing the 99th percentile past the shoulder, and is capped per source. A JPEG has
    8 bits of shadow to work with, so it gets a lower bar to clear and a smaller cap than a RAW."""
    kind = "raw" if raw else "jpeg"
    if not ex.get("recover", True) or stats["key"] >= ex[f"{kind}_dark_key"]:
        return 0.0
    ev = np.log2(ex["target_key"] / stats["key"])
    ev = min(ev, ex[f"{kind}_max_ev"], np.log2(ex["highlight_cap"] / max(stats["p99"], 1e-6)))
    return round(float(ev), 2) if ev >= ex["min_ev"] else 0.0


def _lift(lin: np.ndarray, ev: float, knee: float = 0.6) -> np.ndarray:
    """Linear gain, then a soft shoulder above `knee` so lifted highlights roll off instead of clipping. Below the knee
    the gain is exactly linear, so local contrast ratios (what the focus metrics measure) are left as they were."""
    x = lin * np.float32(2.0 ** ev)
    over = x > knee
    x[over] = knee + (1 - knee) * np.tanh((x[over] - knee) / (1 - knee))
    return x


def _lift_8bit(im: Image.Image, ev: float) -> Image.Image:
    lin = _srgb_to_linear(np.asarray(im, np.float32) / 255)
    return Image.fromarray((_linear_to_srgb(_lift(lin, ev)) * 255 + 0.5).astype(np.uint8))


def _flip(im: Image.Image, flip: int) -> Image.Image:
    if flip == 3:
        return im.rotate(180)
    if flip == 5:
        return im.rotate(90, expand=True)
    if flip == 6:
        return im.rotate(-90, expand=True)
    return im


def _load_raw(path: Path, ex: Optional[dict]) -> tuple[Image.Image, Optional[dict]]:
    import rawpy
    info = None
    with rawpy.imread(str(path)) as raw:
        flip = raw.sizes.flip
        try:
            thumb = raw.extract_thumb()
            if thumb.format == rawpy.ThumbFormat.JPEG:
                im = Image.open(io.BytesIO(thumb.data))
                im.load()
                im = im.convert("RGB")
            else:
                im = Image.fromarray(thumb.data).convert("RGB")
            # Embedded previews are sometimes tiny; fall back to a real demosaic if so.
            if min(im.size) < 1200:
                raise ValueError("thumb too small")
            if ex is not None:
                st = exposure_stats(im)
                ev = plan_gain(st, ex, raw=True)
                if ev > 0:
                    im = _lift_8bit(im, ev)
                    info = {"ev": ev, "source": "raw", **{k: round(v, 5) for k, v in st.items()}}
        except Exception:
            rgb = raw.postprocess(half_size=True, use_camera_wb=True, no_auto_bright=False, output_bps=8)
            im = Image.fromarray(rgb)
            flip = 0  # postprocess already applies orientation
    return _flip(im, flip), info


def load(path: Path, exposure: Optional[dict] = None) -> tuple[Image.Image, Optional[dict]]:
    """Full-resolution RGB image with EXIF orientation applied, and what was done to its exposure (None if nothing).

    With an `exposure` config, an underexposed frame is lifted before anything sees it: detection, the metrics, the
    frame and crop the vision model gets, and the viewer. See plan_gain for when and how far."""
    if path.suffix.lower() in RAW_EXT:
        return _load_raw(path, exposure)
    im = Image.open(path)
    im = ImageOps.exif_transpose(im).convert("RGB")
    info = None
    if exposure is not None:
        st = exposure_stats(im)
        ev = plan_gain(st, exposure, raw=False)
        if ev > 0:
            im = _lift_8bit(im, ev)
            info = {"ev": ev, "source": "jpeg", **{k: round(v, 5) for k, v in st.items()}}
    return im, info


def load_rgb(path: Path, exposure: Optional[dict] = None) -> Image.Image:
    return load(path, exposure)[0]


def resize_long_edge(im: Image.Image, long_edge: int) -> Image.Image:
    w, h = im.size
    if max(w, h) <= long_edge:
        return im
    s = long_edge / max(w, h)
    return im.resize((max(1, round(w * s)), max(1, round(h * s))), Image.LANCZOS)


def to_jpeg(im: Image.Image, quality: int) -> bytes:
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=quality)
    return buf.getvalue()


def crop_box(im: Image.Image, box: tuple[float, float, float, float], pad: float, size: int) -> tuple[Image.Image, tuple[int, int, int, int]]:
    """Crop `box` (x0,y0,x1,y1 in pixels) with padding at native resolution; only downscale if larger than `size`."""
    W, H = im.size
    x0, y0, x1, y1 = box
    bw, bh = x1 - x0, y1 - y0
    x0, x1 = max(0, x0 - bw * pad), min(W, x1 + bw * pad)
    y0, y1 = max(0, y0 - bh * pad), min(H, y1 + bh * pad)
    # widen very tall boxes a bit so faces/helmets get context
    if (y1 - y0) > 2.2 * (x1 - x0):
        extra = ((y1 - y0) / 2.2 - (x1 - x0)) / 2
        x0, x1 = max(0, x0 - extra), min(W, x1 + extra)
    ib = (int(x0), int(y0), int(x1), int(y1))
    crop = im.crop(ib)
    return resize_long_edge(crop, size), ib
