"""Shared fixtures for the workflow tests: a tiny synthetic shoot, a stub person detector and a stub vision model.

The shoot is three frames plus a RAW twin and a sidecar:
  photos/sharp.jpg        landscape, fine texture: the local stage scores it tier 3
  photos/sharp.xmp        a Lightroom sidecar (3 stars, Green)
  photos/soft.jpg         landscape, heavily blurred: tier 0
  photos/soft.CR2         RAW twin of soft.jpg, skipped by the default skip_raw_dupes
  photos/day_2/empty.jpg  portrait: the stub detector finds nobody in portrait frames
"""
from __future__ import annotations
import json
from pathlib import Path

import cv2
import numpy as np
import pytest
from PIL import Image

from photosort import backends, config, schema
from photosort.backends import Result

PERSON_BOX = [300.0, 100.0, 600.0, 580.0]    # in detector (downscaled) pixels; frames are below detect_long_edge


def texture(w: int, h: int, sigma: float = 0.0, seed: int = 0) -> Image.Image:
    rng = np.random.default_rng(seed)
    g = rng.random((h, w), dtype=np.float32)
    if sigma:
        g = cv2.GaussianBlur(g, (0, 0), sigma)
    return Image.fromarray((np.dstack([g] * 3) * 255).astype(np.uint8))


XMP = '<x:xmpmeta><rdf:Description xmp:Rating="3" xmp:Label="Green"/></x:xmpmeta>'


def make_shoot(root: Path) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    texture(900, 600).save(root / "sharp.jpg", quality=95)
    (root / "sharp.xmp").write_text(XMP)
    texture(900, 600, sigma=8, seed=1).save(root / "soft.jpg", quality=95)
    (root / "soft.CR2").write_bytes(b"not really a raw")
    (root / "day_2").mkdir(exist_ok=True)
    texture(400, 600, seed=2).save(root / "day_2" / "empty.jpg", quality=95)
    return root


class FakeDetector:
    """Stands in for local.Detector: one upright person in landscape frames, nobody in portrait ones."""
    device = "cpu"

    def __init__(self):
        self.calls = 0

    def __call__(self, im):
        self.calls += 1
        if im.size[0] <= im.size[1]:
            return []
        return [{"box": list(PERSON_BOX), "conf": 0.9, "kp": None, "kpc": None}]


def vlm_answer(**over) -> dict:
    d = {"focus_tier": 1, "focus_notes": "soft", "primary_subject": "rider_action", "people_count": 1,
         "composition": "full_body", "subject_placement": "center", "action": "carving",
         "keywords": ["onewheel", "trail", "dirt", "helmet", "forest"], "adjectives": ["dynamic", "green", "calm"],
         "description": "a rider", "quality_remarks": "fine", "quality_score": 4, "keeper": True}
    d.update(over)
    return schema.validate(d)


class FakeBackend:
    """A sync vision backend that answers every image with vlm_answer(), or per image id via `answers`."""
    name, default_model, sync, base_url = "fake", "fake-vl", True, "http://fake"

    def __init__(self, answers: dict | None = None):
        self.answers = answers or {}
        self.items = []

    def classify(self, item, model, cfg):
        self.items.append(item)
        ans = self.answers.get(int(item.key), vlm_answer())
        if isinstance(ans, str):
            return Result(item.key, error=ans, usage={"in": 10, "out": 0})
        return Result(item.key, data=ans, usage={"in": 1000, "out": 200, "seconds": 0.5, "decode_s": 0.4,
                                                  "prefill_s": 0.1, "tok_s": 500.0})

    def build_request(self, item, model, cfg):
        return {"model": model, "images": [item.frame], "text": item.context}

    def estimate(self, model, n_with_crop, n_without, cfg):
        return {"model": model, "input_tokens": 1, "output_tokens": 1, "interactive_usd": 0.0, "batch_usd": 0.0, "priced": True}


def write_config(workdir: Path, **over):
    """Config for tests: no face model (it would download weights), everything else default."""
    workdir.mkdir(parents=True, exist_ok=True)
    cfg = json.loads(json.dumps(config.DEFAULTS))
    cfg["focus"]["use_eyes"] = False
    cfg["workers"] = 2
    cfg.update(over)
    (workdir / "config.json").write_text(json.dumps(cfg))
    return cfg


@pytest.fixture
def shoot(tmp_path):
    return make_shoot(tmp_path / "photos")


@pytest.fixture
def fake_backend(monkeypatch):
    be = FakeBackend()
    monkeypatch.setattr(backends, "get", lambda *a, **k: be)
    return be
