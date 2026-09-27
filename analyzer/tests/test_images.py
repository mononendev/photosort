"""Exposure recovery: which frames get lifted, by how much, and that the lift leaves the focus metric's scale alone."""
import numpy as np
from PIL import Image

from photosort_analyzer import images as I, metrics as local

EX = {"recover": True, "target_key": 0.08, "highlight_cap": 0.9, "min_ev": 0.5,
      "raw_dark_key": 0.03, "raw_max_ev": 4.0, "jpeg_dark_key": 0.015, "jpeg_max_ev": 1.5}


def _scene(scale: float, highlight: float = 0.0) -> Image.Image:
    """A textured frame in linear light times `scale`, sRGB-encoded; optional bright patch (a lamp, a fire)."""
    rng = np.random.default_rng(0)
    lin = np.clip(rng.lognormal(np.log(0.1), 0.8, (400, 600)), 0, 1) * scale
    if highlight:
        lin[:60, :100] = highlight   # 2.5% of the frame: above the 99th percentile
    rgb = (I._linear_to_srgb(lin.astype(np.float32)) * 255 + 0.5).astype(np.uint8)
    return Image.fromarray(np.dstack([rgb] * 3))


def test_normal_frames_are_left_alone():
    st = I.exposure_stats(_scene(1.0))
    assert I.plan_gain(st, EX, raw=True) == 0 and I.plan_gain(st, EX, raw=False) == 0


def test_dark_raw_is_lifted_and_capped():
    st = I.exposure_stats(_scene(1 / 64))
    assert I.plan_gain(st, EX, raw=True) == EX["raw_max_ev"]
    assert 0 < I.plan_gain(st, EX, raw=False) <= EX["jpeg_max_ev"]   # a JPEG is lifted less


def test_bright_highlights_hold_the_lift_back():
    """A night frame lit by fire or stage lights: the dark surround is intentional, the highlights already sit high."""
    st = I.exposure_stats(_scene(1 / 64, highlight=0.95))
    assert I.plan_gain(st, EX, raw=True) == 0


def test_recover_off():
    assert I.plan_gain(I.exposure_stats(_scene(1 / 64)), {**EX, "recover": False}, raw=True) == 0


def test_jpeg_load_records_the_lift(tmp_path):
    p = tmp_path / "dark.jpg"
    _scene(1 / 64).save(p, quality=95)
    im, info = I.load(p, EX)
    assert info["source"] == "jpeg" and info["ev"] > 0
    assert np.asarray(im).mean() > np.asarray(I.load_rgb(p)).mean()
    assert I.load(p)[1] is None   # no config, no lift


def test_lift_keeps_the_contrast_normalized_laplacian():
    """Below the knee the lift is a pure gain, so the metric moves only through its contrast floor (EPS)."""
    im = _scene(1 / 16)
    g0 = np.asarray(im.convert("L"), np.float32) / 255
    g1 = np.asarray(I._lift_8bit(im, 2.0).convert("L"), np.float32) / 255
    t0, t1 = local.sharpness_parts(g0), local.sharpness_parts(g1)
    r0, r1 = t0["lap_var"] / t0["gray_var"], t1["lap_var"] / t1["gray_var"]
    assert abs(r1 / r0 - 1) < 0.1
    assert t1["gray_var"] > 2 * t0["gray_var"]   # the region clears the EPS floor it was stuck under
