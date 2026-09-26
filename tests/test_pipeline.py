"""Job counters: a local re-analysis whose vision stage has nothing new must keep its local done/total."""
import json

from photosort import backends, local, pipeline, schema
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
            if hasattr(progress, "finish"):
                progress.finish(i, "boom" if i == ids_paths[-1][0] else None)
            progress.update(1)
        return len(ids_paths), 1  # one error, to check it survives the vlm stage

    class Res:
        def __init__(self, key): self.key, self.data, self.usage, self.error = key, {"focus_tier": 2}, {}, None

    class Backend:
        sync, default_model = True, "fake"
        def classify(self, item, model, cfg): return Res(item.key)

    monkeypatch.setattr(local, "run_local", fake_local)
    monkeypatch.setattr(backends, "get", lambda *a, **k: Backend())
    monkeypatch.setattr(schema, "context_text", lambda d: "")
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


def test_vlm_items_recorded_with_usage_and_stages(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    jid = db.add_job([str(photos)], {"vlm": True})
    r.run_job(db.job(jid))
    items = db.job_items(jid, stage="vlm")
    assert len(items) == 2 and all(i["error"] is None and i["seconds"] >= 0 for i in items)
    assert db.job_item_count(jid, "vlm") == 2 and db.in_flight(jid) == []
    stages = json.loads(db.job(jid)["stages_json"])
    assert list(stages) == ["scan", "local", "vlm"]
    assert stages["vlm"]["model"] == "fake" and stages["vlm"]["done"] == 2
    assert all("finished" in s for s in stages.values())


def test_progress_tracks_in_flight(tmp_path):
    db = DB(tmp_path / "db.sqlite")
    jid = db.add_job([], {})
    p = pipeline._Progress(db, jid, "local")
    p.start(7, "/x/a.jpg")
    assert [(a["id"], a["stage"]) for a in db.in_flight(jid)] == [(7, "local")] and db.job_item_count(jid) == 0
    p.finish(7, "boom")
    assert db.in_flight(jid) == [] and db.job_items(jid, errors=True)[0]["error"] == "boom"


# ---- rolling restarts: two runners on one database -----------------------------------------------------

def test_second_runner_leaves_a_live_job_alone(tmp_path, monkeypatch):
    db, old, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    jid = db.add_job([str(photos)], {"vlm": True})
    assert db.claim_job(jid, old.owner)
    new = pipeline.JobRunner(db, old.cfg, tmp_path, photos)
    assert db.requeue_stale(pipeline.LEASE_TTL_S) == []          # what the new server does on its loop
    new.run_job(db.job(jid))                                      # and it can't claim it either
    assert db.job_lease(jid) == ("running", old.owner)
    assert not new._job(jid, done=99) and db.job(jid)["done"] == 0


def test_stale_claim_is_requeued_and_in_flight_dropped(tmp_path):
    db = DB(tmp_path / "db.sqlite")
    jid, cid = db.add_job([], {}), db.add_job([], {})
    db.claim_job(jid, "dead"); db.claim_job(cid, "dead")
    db.start_job_item(jid, 1, "vlm", 0.0)
    db.cancel_job(cid)
    db.update_job(jid, heartbeat=0.0); db.update_job(cid, heartbeat=0.0)
    assert sorted(db.requeue_stale(pipeline.LEASE_TTL_S)) == [jid, cid]
    assert db.job_lease(jid) == ("queued", None) and db.in_flight(jid) == []
    assert db.job_lease(cid) == ("cancelled", None)


def test_shutdown_mid_vlm_hands_back_and_next_runner_resumes(tmp_path, monkeypatch):
    db, old, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    jid = db.add_job([str(photos)], {"vlm": True})
    classify = backends.get().classify
    calls = []

    def first_then_stop(item, model, cfg):   # the server gets SIGTERM while the first image is with the model
        calls.append(item.key)
        old.stop_event.set()
        return classify(item, model, cfg)

    monkeypatch.setattr(backends, "get", lambda *a, **k: type("B", (), {"sync": True, "default_model": "fake",
                                                                        "classify": staticmethod(first_then_stop)})())
    old.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["owner"], j["done"], j["total"]) == ("queued", None, 1, 2)   # the in-flight image finished
    started = j["started"]

    new = pipeline.JobRunner(db, old.cfg, tmp_path, photos)
    new._detector = object()
    new.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["done"], j["total"], j["errors"]) == ("done", 2, 2, 1)
    assert j["started"] == started and len(calls) == 2 and len(set(calls)) == 2       # nothing tagged twice
    assert db.job_item_count(jid, "vlm") == 2 and "finished" in json.loads(j["stages_json"])["vlm"]


def test_runner_that_lost_its_job_writes_nothing(tmp_path, monkeypatch):
    db, old, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    jid = db.add_job([str(photos)], {"vlm": True})
    db.claim_job(jid, old.owner)
    db.update_job(jid, heartbeat=0.0)
    db.requeue_stale(pipeline.LEASE_TTL_S)
    db.claim_job(jid, "new-server")
    old._stopped(jid); old._finish(jid, "done")
    assert db.job_lease(jid) == ("running", "new-server")


def test_old_database_is_migrated_and_indexed(tmp_path):
    import sqlite3
    p = tmp_path / "old.db"
    c = sqlite3.connect(p)   # the first schema: no folder, overrides, Lightroom or truth columns
    c.executescript("CREATE TABLE images (id INTEGER PRIMARY KEY, path TEXT UNIQUE NOT NULL, size INTEGER, mtime REAL, "
                    "local_json TEXT, batch_id TEXT, vlm_json TEXT, vlm_usage TEXT, error TEXT);"
                    "INSERT INTO images(path, local_json) VALUES ('/p/a.jpg', '{\"local_tier\": 1}');")
    c.commit(); c.close()
    db = DB(p)
    assert db.row(1)["folder"] == "/p" and db.row(1)["lr_json"] is None
    idx = {r[0] for r in db.conn.execute("SELECT name FROM sqlite_master WHERE type='index'")}
    assert {"idx_final_tier", "idx_tier_lr", "idx_final_tier_strict", "idx_tier_lr_local"} <= idx
    from photosort.db import FINAL_TIER_SQL, final_tier_sql
    plan = db.conn.execute(f"EXPLAIN QUERY PLAN SELECT {FINAL_TIER_SQL} t, COUNT(*) FROM images "
                           "WHERE local_json IS NOT NULL GROUP BY t").fetchall()
    assert "USING INDEX idx_final_tier'" in str([tuple(r) for r in plan])
    plan = db.conn.execute(f"EXPLAIN QUERY PLAN SELECT {final_tier_sql('strict')} t, COUNT(*) FROM images "
                           "WHERE local_json IS NOT NULL GROUP BY t").fetchall()
    assert "idx_final_tier_strict" in str([tuple(r) for r in plan])
