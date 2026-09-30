"""ONNX pose detection: the letterbox and NMS match ultralytics', and the yolo11n export finds the people the golden
fixtures (made with the original torch model) have, in the same places."""
import json
from pathlib import Path

import numpy as np
import pytest

from photosort_analyzer import detectors, images as I, measure as MS, models
from photosort_analyzer.detectors.onnx_ import letterbox, nms

ROOT = Path(__file__).parents[2]


def test_letterbox_pads_to_a_multiple_of_32_centered():
    img = np.zeros((853, 1280, 3), np.uint8)
    out, r, (left, top) = letterbox(img, 1280, auto=True)
    assert out.shape == (864, 1280, 3) and r == 1.0 and (left, top) == (0, 5)
    assert (out[0, 0] == 114).all() and (out[5, 0] == 0).all()
    sq, r, _ = letterbox(img, 640, auto=False)
    assert sq.shape == (640, 640, 3) and r == 0.5


def test_nms_keeps_the_best_of_overlapping_boxes():
    boxes = np.array([[0, 0, 10, 10], [1, 1, 11, 11], [20, 20, 30, 30], [0, 0, 10, 10.5]], float)
    scores = np.array([0.9, 0.8, 0.7, 0.95])
    assert nms(boxes, scores, 0.7).tolist() == [3, 2]       # 0 and 1 overlap 3 above 0.7
    assert nms(boxes, scores, 0.99).tolist() == [3, 0, 1, 2]


def _golden():
    for f in sorted((ROOT / "testdata/golden").glob("*.json")):
        yield json.loads(f.read_text())


@pytest.mark.skipif("yolo11n-pose" not in models.manifest(), reason="yolo11n-pose not installed (photosort models get)")
@pytest.mark.parametrize("doc", list(_golden()), ids=lambda d: d["path"].rsplit("/", 1)[-1])
def test_onnx_export_reproduces_the_torch_detections(doc, tmp_path):
    cfg = doc["config"]
    req = {"id": 1, "path": str(ROOT / doc["path"]), "cache_dir": str(tmp_path), "exposure": cfg["exposure"],
           "detect_long_edge": cfg["detect_long_edge"], "min_person_frac": cfg["min_person_frac"],
           "dedup": {k: cfg[k] for k in ("dedup_iou", "dedup_head_iou", "dedup_head_tol")},
           "detect": {"model": "yolo11n-pose", "imgsz": cfg["detect_long_edge"], "conf": cfg["detect_conf"]}}
    m = MS.measure(req)
    MS.sessions.pop(m["token"])
    want = doc["measure"]["people"]
    assert [list(p["box"]) for p in m["people"]] == [p["box"] for p in want]
    for got, exp in zip(m["people"], want):
        assert abs(got["conf"] - exp["conf"]) < 1e-3
        assert list(got["head"]) == exp["head"] and got["sharp_head"] == pytest.approx(exp["sharp_head"], rel=1e-6)


@pytest.mark.skipif(not models.manifest(), reason="no pose models installed")
def test_every_installed_model_runs(tmp_path):
    im = I.resize_long_edge(I.load(ROOT / "dev-data/zidane_sharp.jpg")[0], 1280)
    for m in detectors.installed():
        dets = detectors.get(m["name"])(im)
        assert dets and all(len(d["kp"]) == 17 and len(d["kpc"]) == 17 for d in dets), m["name"]
        assert all(0 <= d["box"][0] < d["box"][2] <= im.size[0] for d in dets), m["name"]


def test_sessions_evict_past_max():
    s = MS._Sessions()
    s.max = 2
    toks = [s.put({"n": i}) for i in range(3)]
    with pytest.raises(MS.Gone):
        s.pop(toks[0])  # oldest evicted: its finalize never came
    assert s.pop(toks[2]) == {"n": 2}
