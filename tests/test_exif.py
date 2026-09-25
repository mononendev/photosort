"""EXIF read (JPEG written by Pillow, parsed by exifread) and the static focus prior."""
from PIL import Image
from PIL.ExifTags import Base as T

from photosort import exif, local


def _jpeg(tmp_path, f, shutter, focal, iso=800):
    ex = Image.Exif()
    ex[T.Model] = "Canon EOS R6"
    ifd = ex.get_ifd(0x8769)
    ifd[T.FNumber] = f
    ifd[T.ExposureTime] = shutter
    ifd[T.FocalLength] = focal
    ifd[T.ISOSpeedRatings] = iso
    p = tmp_path / "a.jpg"
    Image.new("RGB", (64, 64)).save(p, exif=ex.tobytes())
    return p


def test_read_jpeg_exif(tmp_path):
    d = exif.read(_jpeg(tmp_path, 1.4, 1 / 30, 85))
    assert d["camera"] == "Canon EOS R6"
    assert abs(d["f_number"] - 1.4) < 1e-3 and abs(d["shutter_s"] - 1 / 30) < 1e-4
    assert d["focal_mm"] == 85 and d["iso"] == 800


def test_read_missing_is_empty(tmp_path):
    p = tmp_path / "b.jpg"
    Image.new("RGB", (32, 32)).save(p)
    assert exif.read(p) == {}
    assert exif.prior({})["summary"] is None


def test_prior_rules():
    slow = exif.prior({"f_number": 1.4, "shutter_s": 1 / 30, "focal_mm": 85})
    assert slow["dof_risk"] == "high" and slow["motion_risk"] == "high" and slow["shake_stops"] > 1
    fast = exif.prior({"f_number": 2.8, "shutter_s": 1 / 2000, "focal_35mm": 200})
    assert fast["dof_risk"] == "high" and fast["motion_risk"] == "low"   # 71mm pupil: thin plane even at f/2.8
    assert exif.prior({"f_number": 5.6, "focal_35mm": 24})["dof_risk"] == "low"
    assert exif.prior({"f_number": 2.2, "focal_35mm": 135})["dof_risk"] == "high"
    # crop factor turns 50mm into 80mm-equivalent
    assert exif.prior({"shutter_s": 1 / 80, "focal_mm": 50}, {"crop_factor": 1.6})["shake_stops"] == 0.0


def test_slow_shutter_demotes_only_borderline_tier2():
    thr = {"tier2_min": 0.03, "tier1_min": 0.01}
    risky = {"motion_risk": "high"}
    assert local.local_tier({"sharp_head": 0.035}, [], thr, risky) == (1, "borderline_sharp_slow_shutter")
    assert local.local_tier({"sharp_head": 0.06}, [], thr, risky)[0] == 2      # clearly sharp (panned) wins
    assert local.local_tier({"sharp_head": 0.035}, [], thr, {"motion_risk": "low"})[0] == 2
