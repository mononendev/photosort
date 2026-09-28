"""The HTTP protocol the Go backend drives: measure, finalize in a chosen order, expiry, full render, focus debug."""
from pathlib import Path

import numpy as np
import pytest
from fastapi.testclient import TestClient
from PIL import Image

from photosort_analyzer import detectors, server

ROOT = Path(__file__).parents[2]
IMG = ROOT / "dev-data" / "zidane_sharp.jpg"


class TwoPeople:
    """Stands in for a pose model: two fixed detections, in the detector input's pixels."""
    name = "fake"

    def __call__(self, im):
        w, h = im.size
        kp = lambda cx, cy: [[cx, cy - 40], [cx + 8, cy - 48], [cx - 8, cy - 48]] + [[cx, cy]] * 14
        return [{"box": [0.1 * w, 0.2 * h, 0.4 * w, 0.95 * h], "conf": 0.9, "kp": kp(0.25 * w, 0.35 * h), "kpc": [0.9] * 17},
                {"box": [0.6 * w, 0.1 * h, 0.9 * w, 0.9 * h], "conf": 0.8, "kp": kp(0.75 * w, 0.25 * h), "kpc": [0.9] * 17}]


@pytest.fixture
def client(monkeypatch):
    monkeypatch.setattr(detectors, "get", lambda *a, **k: TwoPeople())
    return TestClient(server.app)


def _measure(client, tmp_path, path=IMG):
    r = client.post("/measure", json={"id": 7, "path": str(path), "cache_dir": str(tmp_path), "detect_long_edge": 640,
                                      "min_person_frac": 0.0015, "detect": {"model": "fake"}})
    assert r.status_code == 200, r.text
    return r.json()


def test_measure_then_finalize_in_the_backends_order(client, tmp_path):
    m = _measure(client, tmp_path)
    assert len(m["people"]) == 2 and m["width"] == Image.open(IMG).size[0]
    assert (tmp_path / "7.jpg").exists() and (tmp_path / "7_thumb.jpg").exists()
    p = m["people"][1]
    assert {"box", "head", "torso", "upper", "sharp_head", "priority", "terms"} <= p.keys()
    assert "sharp_eye" not in p   # eye bands wait for the order
    r = client.post("/finalize", json={"token": m["token"], "order": [1, 0], "eye_max_people": 1, "use_face": False})
    assert r.status_code == 200, r.text
    f = r.json()
    assert list(f["eyes"]) == ["1"]                       # only the first eye_max_people, by index
    assert f["crop_box"] and (tmp_path / "7_crop.jpg").exists()
    # the crop follows the primary chosen by the backend, person 1
    assert f["crop_box"][0] >= m["people"][1]["upper"][0] - 0.15 * (m["people"][1]["upper"][2] - m["people"][1]["upper"][0]) - 1


def test_finalize_twice_or_late_is_gone(client, tmp_path):
    m = _measure(client, tmp_path)
    assert client.post("/finalize", json={"token": m["token"], "order": [0, 1]}).status_code == 200
    assert client.post("/finalize", json={"token": m["token"], "order": [0, 1]}).status_code == 410


def test_finalize_rejects_a_bad_order(client, tmp_path):
    m = _measure(client, tmp_path)
    assert client.post("/finalize", json={"token": m["token"], "order": [0, 0]}).status_code == 400


def test_nobody_removes_a_stale_crop(client, tmp_path, monkeypatch):
    monkeypatch.setattr(detectors, "get", lambda *a, **k: (lambda im: []))
    (tmp_path / "7_crop.jpg").write_bytes(b"old")
    m = _measure(client, tmp_path)
    f = client.post("/finalize", json={"token": m["token"], "order": []}).json()
    assert f["crop_box"] is None and not (tmp_path / "7_crop.jpg").exists()


def test_without_a_cache_dir_the_jpegs_come_back_in_the_answer(client, tmp_path):
    import base64
    r = client.post("/measure", json={"id": 7, "path": str(IMG), "detect_long_edge": 640, "min_person_frac": 0.0015,
                                      "detect": {"model": "fake"}})
    assert r.status_code == 200, r.text
    m = r.json()
    assert set(m["files"]) == {"7.jpg", "7_thumb.jpg"}
    assert base64.b64decode(m["files"]["7.jpg"])[:2] == b"\xff\xd8"
    f = client.post("/finalize", json={"token": m["token"], "order": [0, 1], "use_face": False}).json()
    assert f["files"]["7_crop.jpg"] and f["files"]["7_full.jpg"] is None   # None: the backend removes its copy
    assert not list(tmp_path.iterdir())


def test_the_photos_root_confines_paths(client, monkeypatch):
    monkeypatch.setattr(server, "PHOTOS_ROOT", str(ROOT / "analyzer"))
    assert client.post("/render-full", json={"path": str(IMG)}).status_code == 403


def test_render_full_and_focus_debug(client, tmp_path):
    r = client.post("/render-full", json={"path": str(IMG)})
    assert r.status_code == 200 and r.headers["content-type"] == "image/jpeg"
    local = {"people": [{"head": [100, 100, 200, 200], "eye": [120, 130, 180, 150]}]}
    d = client.post("/focus-debug", json={"path": str(IMG), "local": local}).json()
    assert d["width"] == Image.open(IMG).size[0] and d["people"][0]["head"]["laplacian"]["value"] > 0


def test_missing_file_is_404(client, tmp_path):
    assert client.post("/render-full", json={"path": str(tmp_path / "nope.jpg")}).status_code == 404


def test_health(client):
    h = client.get("/health").json()
    assert h["ok"] and "models" in h
