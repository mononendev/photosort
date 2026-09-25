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
