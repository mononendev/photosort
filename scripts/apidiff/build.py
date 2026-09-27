"""Build the fixture workdir apidiff serves from both backends: every dev-data and testdata image through the old
Python local stage (real detector), plus vision verdicts, overrides, Lightroom sidecars, ground truth and jobs, so
the API has something to say on every route.

Run with the old Python package's interpreter from the repo root.
The old Python package isn't in this tree any more: point PHOTOSORT_LEGACY at a checkout that has it
(git worktree add ../photosort-py a8ba797), and use that checkout's interpreter.
  .venv/bin/python scripts/apidiff/build.py OUT_DIR
It writes OUT_DIR/photos (copies of the images, with sidecars) and OUT_DIR/work (the workdir).
"""
import json
import os
import random
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, os.environ.get("PHOTOSORT_LEGACY", str(ROOT)))

from photosort import config, pipeline, schema  # noqa: E402
from photosort import backends  # noqa: E402
from photosort.backends.base import Result  # noqa: E402
from photosort.db import DB  # noqa: E402


class Backend:
    """A sync vision backend with varied, deterministic answers."""
    name, default_model, sync, base_url = "fake", "fake-vl", True, "http://fake"

    def classify(self, item, model, cfg):
        k = int(item.key)
        if k % 7 == 3:
            return Result(item.key, error="model refused", usage={"in": 800, "out": 0, "seconds": 0.2})
        d = {"focus_tier": k % 4, "focus_notes": f"notes {k}", "primary_subject": schema.SUBJECTS[k % len(schema.SUBJECTS)],
             "people_count": k % 3, "composition": schema.COMPOSITIONS[k % len(schema.COMPOSITIONS)],
             "subject_placement": "center", "action": "riding", "keywords": ["bike", "trail", f"kw{k % 3}", "helmet", "dirt"],
             "adjectives": ["fast", "muddy", "bright"], "description": f"a rider, frame {k}", "quality_remarks": "fine",
             "quality_score": 1 + k % 5, "keeper": k % 2 == 0}
        return Result(item.key, data=schema.validate(d),
                      usage={"in": 1000 + k, "out": 200 + k, "seconds": 0.5, "decode_s": 0.4, "prefill_s": 0.1, "tok_s": 500.0})


def main():
    out = Path(sys.argv[1]).resolve()
    if out.exists():
        shutil.rmtree(out)
    photos, work = out / "photos", out / "work"
    (photos / "day_1").mkdir(parents=True)
    (photos / "day_2" / "a_b").mkdir(parents=True)
    imgs = sorted((ROOT / "dev-data").glob("*.jpg")) + sorted((ROOT / "testdata/images").glob("*.jpg"))
    for i, p in enumerate(imgs):
        dest = photos / ("day_1" if i % 2 else "day_2/a_b") / p.name
        shutil.copy2(p, dest)
    # a Lightroom sidecar on two of them
    for name, rating, label in (("zidane_sharp", 5, "Green"), ("bus_sharp", 2, "Red")):
        for d in photos.rglob(f"{name}.jpg"):
            d.with_suffix(".xmp").write_text(
                f'<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">'
                f'<rdf:Description xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Rating="{rating}" xmp:Label="{label}"/>'
                f'</rdf:RDF></x:xmpmeta>')
    (photos / "day_2" / "notes.txt").write_text("not an image")

    work.mkdir(parents=True)
    cfg = config.load(work)
    db = DB(work / "photosort.db")
    runner = pipeline.JobRunner(db, cfg, work, photos)
    be = Backend()
    backends.get = lambda *a, **k: be
    # one job over everything (local + vlm), one local-only rescan of a folder, one queued, one cancelled
    for paths, opts in (([str(photos)], {"vlm": True}), ([str(photos / "day_1")], {"vlm": False, "rescan": True})):
        jid = db.add_job(paths, {"vlm": True, "skip_tier0": False, "rescan": False, "revlm": False, "retry_errors": False,
                                 "concurrency": 1, "model": None, **opts})
        runner.run_job(db.job(jid))
    db.add_job([str(photos / "day_2")], {"vlm": True})
    c = db.add_job([str(photos / "day_1")], {"vlm": False})
    db.cancel_job(c)

    rows = db.rows("1")
    rnd = random.Random(1)
    for r in rows[::3]:
        rating = rnd.randrange(5)
        db.set_override(r["id"], {"rating": rating, "focus_tier": min(rating, 3), "reviewed": True,
                                  "reviewed_at": 1790000000.0 + r["id"], "group": 1 + r["id"] % 4,
                                  "note": "check" if r["id"] % 2 else None, "keeper": r["id"] % 2 == 1})
    for r in rows[1::4]:
        db.set_truth(r["id"], {"rating": 3, "label": "Yellow", "focus_tier": 2, "keywords": ["focus-2"]})
    # a stale verdict: the local stage re-ran with a lift the model never saw
    r = rows[2]
    d = json.loads(r["local_json"])
    d["exposure"] = {"ev": 1.25, "source": "jpeg", "key": 0.01, "p99": 0.2}
    db.set_local(r["id"], d)
    # the config history: a threshold edit, then a rescore
    before = json.loads(json.dumps(cfg))
    cfg["focus"]["eye_tier3_min"] = 0.055
    (work / "config.json").write_text(json.dumps(cfg, indent=2))
    config.log_change(work, before, json.loads(json.dumps(cfg)), "apidiff")
    print(f"fixture: {len(rows)} images, {len(db.jobs())} jobs in {out}")


if __name__ == "__main__":
    main()
