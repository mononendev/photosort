"""Canon AFInfo2 decoding, placement on the upright frame, and AF-driven primary subject choice."""
from photosort import af, local


def _words(points, mode=2, size=(5184, 3456), in_focus=(), selected=(), primary=None):
    """Build an AFInfo2 word array (unsigned, as exifread returns it) from (x, y, w, h) points."""
    n = len(points)
    nw = (n + 15) // 16
    w = [0, mode, n, n, *size, *size]
    w += [p[2] for p in points] + [p[3] for p in points] + [p[0] for p in points] + [p[1] for p in points]
    for bits in (in_focus, selected):
        words = [0] * nw
        for i in bits:
            words[i // 16] |= 1 << (i % 16)
        w += words
    if primary is not None:
        w.append(primary)
    return [v & 0xFFFF for v in w]


def test_parse_signed_and_bits():
    raw = af.parse_afinfo2(_words([(0, 0, 100, 100), (-1000, 800, 100, 100)], in_focus=[1], selected=[1], primary=1))
    assert raw["af_size"] == [5184, 3456]
    assert raw["points"][1]["x"] == -1000 and raw["points"][1]["y"] == 800
    assert raw["points"][1]["in_focus"] and not raw["points"][0]["in_focus"]
    assert raw["primary_point"] == 1
    assert af.parse_afinfo2([1, 2, 3]) is None


def test_to_frame_landscape_y_up():
    raw = af.parse_afinfo2(_words([(-1000, 800, 100, 100)], selected=[0]))
    out = af.to_frame(raw, 5184, 3456, orientation=1)
    # center 2592+(-1000)=1592, 1728-800=928 (y counts upward)
    assert out["points"][0]["box"] == [1542, 878, 1642, 978]
    assert out["active"] == [0] and out["active_from"] == "selected" and out["mode_name"] == "single-point"


def test_to_frame_portrait_rotation():
    raw = af.parse_afinfo2(_words([(-1000, 800, 100, 50)], in_focus=[0]))
    # Camera rotated 90 CW for display (orientation 6): upright frame is 3456 x 5184.
    b = af.to_frame(raw, 3456, 5184, orientation=6)["points"][0]["box"]
    # sensor box (1542, 903, 1642, 953) -> x' = H - y, y' = x
    assert b == [3456 - 953, 1542, 3456 - 903, 1642]
    b8 = af.to_frame(raw, 3456, 5184, orientation=8)["points"][0]["box"]
    assert b8 == [903, 5184 - 1642, 953, 5184 - 1542]


def test_pick_primary_follows_af():
    big = {"box": [0, 0, 2000, 3000], "head": [800, 0, 1200, 400], "torso": [0, 400, 2000, 1500], "priority": 0.5}
    small = {"box": [3000, 1000, 3400, 2000], "head": [3100, 1000, 3300, 1200], "torso": [3000, 1200, 3400, 1500],
             "priority": 0.05}
    pts = {"points": [{"i": 0, "box": [3150, 1050, 3250, 1150]}], "active": [0]}
    people = [big, small]
    assert local.pick_primary(people, pts, {"af": {"use": True, "min_score": 0.5}}) == "af"
    assert people[0] is small and small["af_score"] == 2.0
    # Point inside two overlapping heads: the one centered on it wins.
    a = {"box": [0, 0, 400, 800], "head": [100, 0, 300, 200], "priority": 0.5}
    b = {"box": [150, 0, 550, 800], "head": [150, 0, 350, 200], "priority": 0.4}
    people = [a, b]
    assert local.pick_primary(people, {"points": [{"i": 0, "box": [240, 90, 260, 110]}], "active": [0]}, {}) == "af"
    assert people[0] is b
    people = [big, small]
    assert local.pick_primary(people, pts, {"af": {"use": False}}) == "priority" and people[0] is big
    # AF on empty background: prominence decides
    people = [small, big]
    assert local.pick_primary(people, {"points": [{"i": 0, "box": [5000, 3300, 5100, 3400]}], "active": [0]}, {}) == "priority"
    assert people[0] is big


def test_read_missing_is_none(tmp_path):
    from PIL import Image
    p = tmp_path / "x.jpg"
    Image.new("RGB", (32, 32)).save(p)
    assert af.read(p, 32, 32) is None


def _tiff(words, orientation=8):
    """Minimal little-endian TIFF: IFD0 (Orientation, ExifIFD) -> Exif IFD (MakerNote) -> maker note IFD (0x0026)."""
    import struct
    b = bytearray(b"II*\x00" + struct.pack("<I", 8))
    ifd0, exif, mn, arr = 8, 8 + 2 + 24 + 4, 8 + 2 + 24 + 4 + 2 + 12 + 4, 8 + 2 + 24 + 4 + 2 + 12 + 4 + 2 + 12 + 4
    b += struct.pack("<H", 2) + struct.pack("<HHIHH", 0x0112, 3, 1, orientation, 0) + struct.pack("<HHII", 0x8769, 4, 1, exif) + b"\0" * 4
    b += struct.pack("<H", 1) + struct.pack("<HHII", 0x927C, 7, 0, mn) + b"\0" * 4
    b += struct.pack("<H", 1) + struct.pack("<HHII", 0x0026, 3, len(words), arr) + b"\0" * 4
    assert len(b) == arr
    return bytes(b + struct.pack(f"<{len(words)}H", *words))


def test_read_tiff_maker_note(tmp_path):
    p = tmp_path / "x.cr2"
    p.write_bytes(_tiff(_words([(0, 0, 171, 171), (1174, 0, 171, 171)], mode=9, in_focus=[1], selected=[1])))
    got, note = af.read_with_note(p, 3456, 5184)
    assert got["mode_name"] == "spot" and got["active"] == [1] and note == "spot, 1 active"
    # Orientation 8 (upright = sensor rotated 90 CCW): sensor-right lands in the upper half of the portrait frame.
    b = got["points"][0]["box"]
    assert (b[1] + b[3]) / 2 < 5184 / 2 - 1000 and abs((b[0] + b[2]) / 2 - 3456 / 2) < 2


def test_manual_focus_has_no_active_points():
    raw = af.parse_afinfo2(_words([(0, 0, 100, 100), (500, 0, 100, 100)], mode=0, selected=[0, 1]))
    out = af.to_frame(raw, 5184, 3456)
    assert out["active"] == [] and out["points"] == [] and out["mode_name"] == "manual focus"


def test_primary_point_padding_ignored():
    # 1D X: the word after the bitmasks is 0 padding, not "point 0"
    raw = af.parse_afinfo2(_words([(0, 0, 100, 100), (500, 0, 100, 100)], in_focus=[1], selected=[1], primary=0))
    assert raw["primary_point"] is None


def test_real_1dx_cr2_if_present():
    import pytest
    from pathlib import Path
    p = Path(__file__).parent.parent / "dev-data" / "cr2" / "IMG_1010.CR2"
    if not p.exists():
        pytest.skip("local CR2 sample not present (dev-data/cr2 is gitignored)")
    got = af.read(p, 3456, 5184)
    assert got["mode_name"] == "spot" and got["active"] == [30] and got["n_points"] == 61


def test_rescore_old_rows_without_priority(tmp_path):
    """Rows analyzed before `priority` existed crashed rescore (KeyError); they must re-score, and AF must land."""
    import json
    from photosort import config
    from photosort.db import DB
    cr2 = tmp_path / "a.cr2"
    cr2.write_bytes(_tiff(_words([(0, 0, 171, 171), (1174, 0, 171, 171)], mode=9, in_focus=[1], selected=[1])))
    bad = tmp_path / "b.jpg"
    bad.write_bytes(b"")
    db = DB(tmp_path / "db.sqlite")
    db.add_paths([cr2, bad])
    # Portrait 3456x5184: point 1 lands around y ~ 1100 near the horizontal middle.
    near = {"box": [1400, 700, 2100, 3000], "head": [1500, 700, 1950, 1150], "torso": [1400, 1150, 2100, 2000],
            "sharp_head": 0.1, "sharp_body": 0.1}
    far = {"box": [100, 2000, 1000, 5000], "head": [300, 2000, 800, 2500], "torso": [100, 2500, 1000, 3500],
           "sharp_head": 0.2, "sharp_body": 0.2}
    rows = {r["path"]: r["id"] for r in db.rows("1")}
    db.set_local(rows[str(cr2)], {"width": 3456, "height": 5184, "people": [far, near], "exif": {}, "local_tier": 0})
    db.set_local(rows[str(bad)], {"width": 10, "height": 10, "people": [None], "exif": {}, "local_tier": 0})
    res = local.rescore(db, config.DEFAULTS)
    assert res["errors"] == 1 and "b.jpg" in res["first_error"]  # the malformed row is reported, not fatal
    d = json.loads(db.row(rows[str(cr2)])["local_json"])
    assert d["af"]["active"] == [1] and d["primary_by"] == "af" and d["people"][0]["head"] == near["head"]
    assert res["af_backfilled"] == 1 and res["primary_changed"] == 1
