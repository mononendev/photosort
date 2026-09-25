"""Job counters: a local re-analysis whose vision stage has nothing new must keep its local done/total."""
import json
from pathlib import Path

from photosort import backends, local, pipeline
from photosort.config import DEFAULTS
from photosort.db import DB


def _runner(tmp_path, monkeypatch, vlm_rows_done: bool):
    photos = tmp_path / "photos"; photos.mkdir()
    for n in ("a.jpg", "b.jpg"):
        (photos / n).write_bytes(b"\xff\xd8\xff")
    db = DB(tmp_path / "db.sqlite")

    def fake_local(db_, cfg, cache, ids_paths, device, progress, should_stop, det):
        for i, _ in ids_paths:
            db_.set_local(i, {"local_tier": 2, "people": []})
            progress.update(1)
        return len(ids_paths), 1  # one error, to check it survives the vlm stage

    class Res:
        def __init__(self, key): self.key, self.data, self.usage, self.error = key, {"focus_tier": 2}, {}, None

    class Backend:
        sync, default_model = True, "fake"
        def classify(self, item, model, cfg): return Res(item.key)

    monkeypatch.setattr(local, "run_local", fake_local)
    monkeypatch.setattr(backends, "get", lambda *a, **k: Backend())
    monkeypatch.setattr(pipeline.schema, "context_text", lambda d: "")
    r = pipeline.JobRunner(db, json.loads(json.dumps(DEFAULTS)), tmp_path, photos)
    r._detector = object()
    (tmp_path / "cache").mkdir()
    if vlm_rows_done:
        db.add_paths([photos / "a.jpg", photos / "b.jpg"])
        for row in db.rows("1"):
            db.set_local(row["id"], {"local_tier": 2, "people": []})
            db.set_vlm(row["id"], {"focus_tier": 2}, {}, None)
    else:
        db.add_paths([photos / "a.jpg", photos / "b.jpg"])
        for row in db.rows("1"):
            (tmp_path / "cache" / f"{row['id']}.jpg").write_bytes(b"x")
    return db, r, photos


def test_rerun_with_nothing_for_vlm_keeps_local_counts(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=True)
    jid = db.add_job([str(photos)], {"vlm": True, "rescan": True})
    r.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["done"], j["total"], j["errors"]) == ("done", 2, 2, 1)
    assert "local 2/2" in j["message"] and "nothing new" in j["message"]


def test_vlm_stage_takes_over_counters_and_adds_errors(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    jid = db.add_job([str(photos)], {"vlm": True})
    r.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["stage"], j["done"], j["total"], j["errors"]) == ("done", "done", 2, 2, 1)
    assert j["message"].startswith("local 2/2 · ")


def test_rows_under_many_files_and_literal_folder_prefix(tmp_path):
    root = tmp_path / "p"
    (root / "a_b").mkdir(parents=True); (root / "axb").mkdir()
    files = [root / f"IMG_{i:04d}.CR2" for i in range(1500)]
    for f in files + [root / "a_b" / "x.jpg", root / "axb" / "y.jpg"]:
        f.write_bytes(b"x")
    db = DB(tmp_path / "db.sqlite")
    db.add_paths(files + [root / "a_b" / "x.jpg", root / "axb" / "y.jpg"])
    got = db.rows_under(files)                      # 1500 OR-pairs used to blow SQLite's depth limit
    assert len(got) == 1500
    assert [r["path"] for r in db.rows_under([root / "a_b"])] == [str(root / "a_b" / "x.jpg")]  # '_' is literal
    assert len(db.rows_under(files[:10] + [root / "axb"], "path LIKE ?", ["%.CR2"])) == 10


def test_job_over_1500_selected_files_runs(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=True)
    many = [photos / f"IMG_{i:04d}.jpg" for i in range(1500)]
    for f in many:
        f.write_bytes(b"\xff\xd8\xff")
    jid = db.add_job([str(f) for f in many], {"vlm": False})
    r.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["done"], j["total"]) == ("done", 1500, 1500)
