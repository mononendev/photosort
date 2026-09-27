"""Golden fixtures pinning the Go backend to the Python stage it replaces.

For each image, one detector run feeds both the old photosort.local.analyze and the analyzer's measure/finalize, so
they see identical detections. Written per image to testdata/golden/<name>.json:

  config      the config both ran with
  path        the image, relative to the repo root
  local       old analyze()'s local_json: what the Go assembly must reproduce exactly
  measure     the analyzer's measure() response (token dropped)
  order       the people order analyze() settled on, as indices into measure.people
  finalize    finalize() for that order

Run from the repo root with an interpreter that has the old package's deps and ultralytics (e.g. the old .venv):

  python analyzer/scripts/golden.py dev-data/*.jpg [--out testdata/golden] [--config config.json]
"""
from __future__ import annotations
import argparse
import json
import shutil
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT), str(ROOT / "analyzer")]

from photosort import config as C, local as L  # noqa: E402  the old stage
from photosort_analyzer import measure as MS  # noqa: E402
from photosort_analyzer.detectors.ultralytics_ import UltralyticsDetector  # noqa: E402


class Recording:
    """Calls the real detector once per image and replays that answer to the second caller."""

    def __init__(self, det):
        self.det, self.last = det, None
        self.name = det.name

    def __call__(self, im):
        if self.last is None:
            self.last = self.det(im)
        return json.loads(json.dumps(self.last))


def request(cfg: dict, path: Path, cache: Path) -> dict:
    return {"id": 1, "path": str(path), "cache_dir": str(cache), "exposure": cfg.get("exposure"),
            "detect_long_edge": cfg["detect_long_edge"], "min_person_frac": cfg["min_person_frac"],
            "dedup": {k: cfg[k] for k in ("dedup_iou", "dedup_head_iou", "dedup_head_tol") if k in cfg},
            "detect": {"model": cfg["detect_model"], "imgsz": cfg["detect_long_edge"], "conf": cfg["detect_conf"]},
            "frame": {"long_edge": cfg["frame_long_edge"], "quality": cfg["frame_quality"]}}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("images", nargs="+", type=Path)
    ap.add_argument("--out", type=Path, default=ROOT / "testdata/golden")
    ap.add_argument("--config", type=Path, help="config.json to merge over the defaults")
    a = ap.parse_args()
    cfg = json.loads(json.dumps(C.DEFAULTS))
    if a.config:
        for k, v in json.loads(a.config.read_text()).items():
            cfg[k] = {**cfg[k], **v} if isinstance(v, dict) and isinstance(cfg.get(k), dict) else v
    a.out.mkdir(parents=True, exist_ok=True)
    det = UltralyticsDetector(cfg["detect_model"], cfg["detect_long_edge"], cfg["detect_conf"], "cpu")
    faces = L.FaceLandmarks(cfg["face_model"], cfg["face_conf"]) if cfg["focus"].get("use_eyes", True) else None
    cache = Path(tempfile.mkdtemp())
    try:
        for p in a.images:
            rec = Recording(det)
            old = L.analyze(p, cfg, rec, faces).data
            m = MS.measure(request(cfg, p, cache), rec)
            token = m.pop("token")
            # People past the first six aren't stored, but every box is in mask_boxes, in the final order.
            boxes = [list(q["box"]) for q in m["people"]]
            order = []
            for b in old["mask_boxes"]:
                order.append(next(i for i, x in enumerate(boxes) if x == list(b) and i not in order))
            fin = MS.finalize({"token": token, "order": order, "eye_max_people": cfg.get("eye_max_people", 4),
                               "use_face": faces is not None, "face_model": cfg["face_model"], "face_conf": cfg["face_conf"],
                               "plane": cfg["focus"].get("use_plane", True),
                               "crop": {"pad": cfg["crop_pad"], "size": cfg["crop_size"], "quality": cfg["crop_quality"]}})
            try:
                rel = p.resolve().relative_to(ROOT).as_posix()
            except ValueError:
                rel = str(p)
            doc = {"config": cfg, "path": rel, "local": old, "measure": m, "order": order, "finalize": fin}
            (a.out / f"{p.name}.json").write_text(json.dumps(doc, indent=1, sort_keys=True))
            print(f"{p.name}: {len(m['people'])} people, tier {old['local_tier']} ({old['local_reason']})")
    finally:
        shutil.rmtree(cache, ignore_errors=True)


if __name__ == "__main__":
    main()
