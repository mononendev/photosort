from __future__ import annotations
import io
import os
from pathlib import Path
from typing import Iterable
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


def _load_raw(path: Path) -> Image.Image:
    import rawpy
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
        except Exception:
            rgb = raw.postprocess(half_size=True, use_camera_wb=True, no_auto_bright=False, output_bps=8)
            im = Image.fromarray(rgb)
            flip = 0  # postprocess already applies orientation
    if flip == 3:
        im = im.rotate(180)
    elif flip == 5:
        im = im.rotate(90, expand=True)
    elif flip == 6:
        im = im.rotate(-90, expand=True)
    return im


def load_rgb(path: Path) -> Image.Image:
    """Full-resolution RGB image with EXIF orientation applied."""
    if path.suffix.lower() in RAW_EXT:
        return _load_raw(path)
    im = Image.open(path)
    im = ImageOps.exif_transpose(im)
    return im.convert("RGB")


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
