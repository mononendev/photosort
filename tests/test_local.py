"""Fast checks that don't need a GPU or network: sharpness metric ordering, tier rules, schema, XMP."""
import json
import numpy as np
import cv2

from photosort import local, schema, sort


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
    rgb = np.asarray(Image.open(Path(__file__).parent.parent / "dev-data" / "zidane_sharp.jpg").convert("RGB"))
    H, W = rgb.shape[:2]
    found = fl.face(rgb, (2719, 1385, 3111, 1891), W, H)
    assert found is not None
    (x1, y1), (x2, y2), score = *found["eyes"], found["score"]
    assert 2800 < x1 < x2 < 3100 and 1450 < y1 < 1650 and 1450 < y2 < 1650 and score > 0.6


THR = {"tier3_min": 0.03, "tier2_min": 0.017, "tier1_min": 0.01,
       "eye_tier3_min": 0.06, "eye_tier2_min": 0.035, "eye_tier1_min": 0.02,
       "hf_tier3_min": 0.03, "hf_tier2_min": 0.017, "hf_tier1_min": 0.01}


def test_local_tier_eye_band_needs_both_metrics():
    sharp = {"sharp_eye": 0.08, "hf_eye": 0.05, "sharp_head": 0.001}
    assert local.local_tier(sharp, [], THR) == (3, "primary_eyes_sharp")          # eyes beat a soft head box
    assert local.local_tier({**sharp, "hf_eye": 0.02}, [], THR) == (2, "primary_eyes_slightly_soft")  # FFT vetoes
    assert local.local_tier({**sharp, "hf_eye": 0.012}, [], THR) == (1, "primary_eyes_soft")
    assert local.local_tier({**sharp, "sharp_eye": 0.01}, [], THR) == (0, "nothing_sharp")
    assert local.local_tier({**sharp, "hf_eye": 0.02}, [], {**THR, "use_hf": False})[0] == 3
    assert local.local_tier({**sharp, "hf_eye": None}, [], THR)[0] == 3           # band too small for FFT
    # no eyes found -> head box against the head thresholds; use_eyes off does the same
    assert local.local_tier({"sharp_eye": None, "sharp_head": 0.05}, [], THR) == (3, "primary_head_sharp")
    assert local.local_tier(sharp, [], {**THR, "use_eyes": False})[0] == 0
    # a sharp secondary face doesn't rescue a missed primary; the reason says so
    assert local.local_tier({"sharp_eye": 0.001, "hf_eye": 0.001}, [sharp], THR) == (0, "secondary_person_sharp")


def test_local_tier_slow_shutter_demotes_borderline_eyes_only():
    slow = {"motion_risk": "high"}
    assert local.local_tier({"sharp_eye": 0.07, "hf_eye": 0.05}, [], THR, slow) == (2, "borderline_sharp_slow_shutter")
    assert local.local_tier({"sharp_eye": 0.2, "hf_eye": 0.05}, [], THR, slow)[0] == 3


def test_local_tier_rules():
    thr = {"tier3_min": 0.03, "tier2_min": 0.017, "tier1_min": 0.01}
    assert local.local_tier(None, [], thr) == (0, "no_people")
    assert local.local_tier({"sharp_head": 0.05}, [], thr)[0] == 3
    assert local.local_tier({"sharp_head": 0.02}, [], thr) == (2, "primary_slightly_soft")
    assert local.local_tier({"sharp_head": 0.012}, [], thr) == (1, "primary_soft")
    assert local.local_tier({"sharp_head": 0.002}, [{"sharp_head": 0.06}], thr) == (0, "secondary_person_sharp")
    assert local.local_tier({"sharp_head": 0.002}, [{"sharp_head": 0.02}], thr)[0] == 0   # a soft bystander doesn't count
    assert local.local_tier({"sharp_head": 0.002}, [], thr)[0] == 0


def test_schema_validate_normalizes():
    d = {k: {"integer": 1, "string": "x", "array": ["A", "a "], "boolean": True}[v["type"]] for k, v in schema.FIELDS.items()}
    d["focus_tier"] = 2; d["primary_subject"] = "nope"; d["composition"] = "full_body"
    out = schema.validate(d)
    assert out["primary_subject"] == "other" and out["keywords"] == ["a"]
    assert "maxLength" in schema.json_schema(max_lengths=True)["properties"]["quality_remarks"]
    assert schema.json_schema(strict=True)["additionalProperties"] is False


def test_final_record_and_xmp(tmp_path):
    row = {"path": str(tmp_path / "a.jpg"), "error": None,
           "local_json": json.dumps({"local_tier": 2, "n_people": 1, "primary_head_sharp": 0.1, "primary_body_sharp": 0.1, "bg_sharp": 0.01, "local_reason": "primary_head_sharp"}),
           "vlm_json": json.dumps({"focus_tier": 1, "primary_subject": "rider_action", "composition": "full_body", "subject_placement": "center", "action": "carving",
                                   "people_count": 1, "keywords": ["onewheel"], "adjectives": ["dynamic"], "description": "d", "focus_notes": "f", "quality_remarks": "q", "quality_score": 4, "keeper": True}),
           "override_json": json.dumps({"focus_tier": 3})}
    rec = sort.final_record(row, "vlm")
    assert rec["focus_tier"] == 3 and rec["review"] and rec["overridden"]
    doc = sort.xmp_for(rec)
    import xml.dom.minidom
    assert xml.dom.minidom.parseString(doc)  # well-formed
    assert "PhotoSort|Focus|focus_3_sharp" in doc and 'xmp:Rating="4"' in doc


def test_debugviz_reproduces_stored_metrics():
    from photosort import debugviz
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
