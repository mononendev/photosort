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


def test_local_tier_rules():
    thr = {"tier2_min": 0.03, "tier1_min": 0.01}
    assert local.local_tier(None, [], thr) == (0, "no_people")
    assert local.local_tier({"sharp_head": 0.05}, [], thr)[0] == 2
    assert local.local_tier({"sharp_head": 0.02}, [], thr)[0] == 1
    assert local.local_tier({"sharp_head": 0.002}, [{"sharp_head": 0.06}], thr) == (1, "secondary_person_sharp")
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
           "override_json": json.dumps({"focus_tier": 2})}
    rec = sort.final_record(row, "vlm")
    assert rec["focus_tier"] == 2 and rec["review"] and rec["overridden"]
    doc = sort.xmp_for(rec)
    import xml.dom.minidom
    assert xml.dom.minidom.parseString(doc)  # well-formed
    assert "PhotoSort|Focus|focus_2_sharp" in doc and 'xmp:Rating="4"' in doc
