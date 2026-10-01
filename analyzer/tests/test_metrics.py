"""The focus metrics: ordering on synthetic blur, geometry, face landmarks, focus plane, dedup."""
import numpy as np
import cv2

from photosort_analyzer import metrics as local


def _pattern(sigma):
    rng = np.random.default_rng(0)
    g = rng.random((256, 256), dtype=np.float32)
    return cv2.GaussianBlur(g, (0, 0), sigma) if sigma else g


def test_sharpness_orders_blur():
    s = [local.sharpness(_pattern(x)) for x in (0.0, 1.5, 4.0)]
    assert s[0] > s[1] > s[2]


def test_sharpness_rejects_tiny_regions():
    assert local.sharpness(np.zeros((10, 10), np.float32)) is None


def _natural(sigma):
    """1/f amplitude spectrum, like real scenes (white noise would overweight the top of the band)."""
    rng = np.random.default_rng(0)
    f = np.hypot(np.fft.fftfreq(256)[:, None], np.fft.fftfreq(256)[None, :])
    spec = np.fft.fft2(rng.random((256, 256))) / np.maximum(f, 1 / 256)
    g = np.real(np.fft.ifft2(spec)).astype(np.float32)
    g = (g - g.min()) / (g.max() - g.min())
    return cv2.GaussianBlur(g, (0, 0), sigma) if sigma else g


def test_hf_ratio_orders_blur_and_reacts_to_slight_blur_faster():
    lap = [local.sharpness(_natural(x)) for x in (0.0, 0.8)]
    hf = [local.hf_ratio(_natural(x)) for x in (0.0, 0.8, 1.5, 4.0)]
    assert hf[0] > hf[1] > hf[2] > hf[3]
    assert hf[1] / hf[0] < lap[1] / lap[0]  # the point of the FFT metric: pickier about the first bit of miss
    assert local.hf_ratio(np.zeros((10, 10), np.float32)) is None
    assert local.hf_ratio(np.full((64, 64), 0.5, np.float32)) is None  # flat: no energy to divide


def test_eye_band_geometry():
    b = local.eye_band((100, 200), (140, 204), 1000, 1000)
    iod = np.hypot(40, 4)
    assert b[0] == int(100 - 0.5 * iod) and b[2] == int(140 + 0.5 * iod)
    assert b[1] < 200 < 204 < b[3]
    assert local.eye_band((100, 200), (104, 200), 1000, 1000) is None  # too small to judge
    assert local.eye_band((2, 5), (40, 5), 50, 50)[0] == 0              # clamped to the frame


def test_face_landmarks_find_eyes_in_head_box():
    import pytest
    from pathlib import Path
    from PIL import Image
    try:
        fl = local.FaceLandmarks(); fl._net()
    except Exception as e:
        pytest.skip(f"face model unavailable: {e}")
    rgb = np.asarray(Image.open(Path(__file__).parents[2] / "dev-data" / "zidane_sharp.jpg").convert("RGB"))
    H, W = rgb.shape[:2]
    found = fl.face(rgb, (2719, 1385, 3111, 1891), W, H)
    assert found is not None
    (x1, y1), (x2, y2), score = *found["eyes"], found["score"]
    assert 2800 < x1 < x2 < 3100 and 1450 < y1 < 1650 and 1450 < y2 < 1650 and score > 0.6


def _scene(head_blur, bg_blur, size=1200):
    """Textured surroundings with a person-shaped patch of texture in the middle, each blurred separately."""
    rng = np.random.default_rng(1)
    tex = lambda s: cv2.GaussianBlur(((rng.random((size // 8, size // 8)) > 0.5) * 1.0).astype(np.float32)
                                     .repeat(8, 0).repeat(8, 1), (0, 0), s) if s else \
        ((rng.random((size // 8, size // 8)) > 0.5) * 1.0).astype(np.float32).repeat(8, 0).repeat(8, 1)
    g = 0.2 + 0.6 * tex(bg_blur)
    person = 0.2 + 0.6 * tex(head_blur)
    g[400:1000, 450:750] = person[400:1000, 450:750]
    p = {"box": (450, 400, 750, 1000), "head": (530, 400, 670, 540), "torso": (450, 540, 750, 800)}
    return g, p


def test_focus_plane_sees_focus_behind_the_head():
    g, p = _scene(head_blur=2.0, bg_blur=0.6)
    pl = local.focus_plane(g, p, g.shape[1], g.shape[0])
    assert pl["head_vs_near"] > 1.0 and pl["near"] < pl["head"]
    g, p = _scene(head_blur=0.6, bg_blur=2.0)
    pl = local.focus_plane(g, p, g.shape[1], g.shape[0])
    assert pl["head_vs_near"] == 0 and pl["head_vs_torso"] < 0.2 and pl["near"] > 1.5
    flat = np.full((1200, 1200), 0.5, np.float32)                       # nothing to measure on the head
    assert local.focus_plane(flat, p, 1200, 1200) is None


def test_debugviz_reproduces_stored_metrics():
    from photosort_analyzer import debugviz
    rng = np.random.default_rng(0)
    g = cv2.GaussianBlur(rng.random((120, 260)).astype(np.float32), (0, 0), 1.5)
    lv, sv = debugviz.laplacian_view(g, local.EYE_MIN_PX), debugviz.spectrum_view(g)
    assert abs(lv["value"] - local.sharpness(g, local.EYE_MIN_PX)) < 1e-6
    assert sv["value"] == local.hf_ratio(g) and abs(sv["band_energy"] / sv["total_energy"] - sv["value"]) < 1e-6
    hm = debugviz.heatmap(rng.random((400, 900)).astype(np.float32))
    assert hm["grid"][0] * hm["tile"] <= 900 and hm["img"].startswith("data:image/png")


def test_metric_terms_reproduce_the_ratios():
    rng = np.random.default_rng(1)
    g = cv2.GaussianBlur(rng.random((90, 700)).astype(np.float32), (0, 0), 1.2)
    t, h = local.sharpness_parts(g, local.EYE_MIN_PX), local.hf_parts(g)
    assert t["px"] == [512, 66]                       # measured after the same 512 px downscale
    assert t["lap_var"] / (t["gray_var"] + local.EPS) == local.sharpness(g, local.EYE_MIN_PX)
    assert h["band_e"] / h["total_e"] == local.hf_ratio(g)
    assert local.sharpness_parts(g[:10], local.EYE_MIN_PX) is None and local.hf_parts(np.full((64, 64), 0.5, np.float32)) is None


def _det(box, conf, head=None):
    kp = [[0.0, 0.0]] * 17
    kpc = [0.0] * 17
    for i, p in zip(local.HEAD_KP, head or []):
        kp[i], kpc[i] = list(p), 0.9
    return {"box": list(box), "conf": conf, "kp": kp, "kpc": kpc}


def test_dedup_drops_shifted_duplicate_of_one_rider():
    # Two boxes on one rider at IoU ~0.6 (below YOLO's 0.7 NMS), heads within ~50px on a ~900px box.
    a = _det((480, 685, 1327, 1765), 0.9, [(808, 700), (788, 688), (833, 690)])
    b = _det((390, 493, 1265, 1590), 0.6, [(830, 668), (800, 637), (864, 637)])
    assert local.dedup_detections([b, a], {}) == [a]


def test_dedup_keeps_overlapping_people_with_distinct_heads():
    front = _det((400, 300, 900, 1500), 0.9, [(650, 400), (630, 390), (670, 390)])
    behind = _det((600, 250, 1100, 1300), 0.8, [(850, 330), (830, 320), (870, 320)])
    assert len(local.dedup_detections([front, behind], {})) == 2



def _pose(**kps):
    """A pose detection with the given COCO keypoints as name=(x, y, conf); the rest unseen."""
    names = ["nose", "leye", "reye", "lear", "rear", "lsho", "rsho", "lelb", "relb", "lwri", "rwri",
             "lhip", "rhip", "lkne", "rkne", "lank", "rank"]
    kp, kpc = [[0.0, 0.0] for _ in names], [0.0] * len(names)
    for n, (x, y, c) in kps.items():
        kp[names.index(n)], kpc[names.index(n)] = [x, y], c
    return {"box": [0, 0, 100, 100], "conf": 0.9, "kp": kp, "kpc": kpc}


# IMG_1061: rider's head turned down and to the side, one eye under the helmet brim (scale 1, full-res pixels).
SIDE_ON = dict(nose=(1297, 574, 0.973), leye=(1314, 499, 0.961), reye=(1252, 532, 0.155),
               lear=(1432, 358, 0.893), rear=(1291, 441, 0.005))
SIDE_ON_FACE = {"eyes": [(1358, 460), (1434, 458)]}   # YuNet's frontal-template eyes, both on the cheek


def test_head_view_side_on_from_one_hidden_eye():
    hv = local.head_view(_pose(**SIDE_ON), 1.0)
    assert hv["view"] == "profile" and hv["near"] == (1314, 499)
    assert 120 < hv["iod"] < 160   # from eye-ear and eye-nose distances


def test_head_view_side_on_when_the_nose_is_past_an_eye():
    # The pose model places the hidden eye with confidence, but the nose sits beyond it: side-on all the same.
    hv = local.head_view(_pose(nose=(95, 110, 0.9), reye=(100, 100, 0.8), leye=(140, 100, 0.9), lear=(190, 105, 0.9)), 1.0)
    assert hv["view"] == "profile" and hv["near"] == (140, 100)


def test_head_view_frontal_and_three_quarter():
    eyes = dict(reye=(100, 100, 0.9), leye=(140, 100, 0.9))
    assert local.head_view(_pose(**eyes, nose=(120, 120, 0.9), lear=(160, 105, 0.9), rear=(80, 105, 0.9)), 1.0)["view"] == "frontal"
    assert local.head_view(_pose(**eyes, nose=(128, 120, 0.9), lear=(160, 105, 0.9), rear=(80, 105, 0.05)), 1.0)["view"] == "turned"
    assert local.head_view(_pose(**eyes, nose=(133, 120, 0.9)), 1.0)["view"] == "turned"
    assert local.head_view(_pose(nose=(120, 120, 0.9)), 1.0) is None   # no eye seen: nothing to say


def test_face_eyes_must_sit_on_the_pose_eyes_unless_the_face_is_sure():
    pose = _pose(reye=(100, 100, 0.9), leye=(140, 100, 0.9))
    assert local.face_agrees({"eyes": [(110, 120), (148, 118)], "score": 0.7}, pose, 1.0)    # off, but within an IOD
    vents = {"eyes": [(100, 40), (140, 42)], "score": 0.7}                                  # helmet vents above the eyes
    assert not local.face_agrees(vents, pose, 1.0)
    assert local.face_agrees({**vents, "score": 0.9}, pose, 1.0)        # a confident face wins: pose eyes slid down
    assert local.face_agrees(vents, _pose(reye=(30, 30, 0.2), leye=(10, 10, 0.1)), 1.0)   # unconfident pose: no evidence


def test_eye_metrics_judge_a_side_on_head_on_its_visible_eye():
    from photosort_analyzer import measure as MS

    class Faces:
        def face(self, *a):
            return {"eyes": SIDE_ON_FACE["eyes"], "score": 0.7, "lm": SIDE_ON_FACE["eyes"] + [(1398, 557)] * 3,
                    "box": [1248, 288, 1532, 665], "search": [398, 0, 2297, 1426]}
    rng = np.random.default_rng(0)
    gray = rng.random((800, 1800)).astype(np.float32)
    e = MS._eye_metrics(_pose(**SIDE_ON), (1031, 160, 1664, 793), 1.0, None, gray, 1800, 800, Faces())
    assert e["eye_view"] == "profile" and e["eye_src"] == "pose" and e["eyes"] == [[1314, 499]]
    x0, y0, x1, y1 = e["eye"]
    assert x0 < 1314 < x1 and y0 < 499 < y1 and e["face"]["rejected"] == "profile"
