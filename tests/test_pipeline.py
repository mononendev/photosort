"""Job counters: a local re-analysis whose vision stage has nothing new must keep its local done/total."""
import json
import time

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


def test_revlm_retags_already_tagged_images(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=True)
    for row in db.rows("1"):
        (tmp_path / "cache" / f"{row['id']}.jpg").write_bytes(b"x")
    jid = db.add_job([str(photos)], {"vlm": True, "revlm": True})
    r.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["done"], j["total"]) == ("done", 2, 2)
    assert "nothing new to tag" not in j["message"]


def test_verdict_on_an_unlifted_frame_is_stale_and_retagged(tmp_path, monkeypatch):
    """A local re-run that lifts a dark frame leaves the model's old verdict about the dark one: it goes back in the
    queue, doesn't count as a disagreement meanwhile, and the new verdict records the lift it saw."""
    from photosort.db import REVIEW_SQL, VLM_STALE_SQL
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=True)
    a, b = [row["id"] for row in db.rows("1")]
    (tmp_path / "cache" / f"{a}.jpg").write_bytes(b"x")
    db.set_local(a, {"local_tier": 3, "people": [], "exposure": {"ev": 4.0, "source": "raw"}})
    db.set_local(b, {"local_tier": 3, "people": []})   # disagrees with the model's 2, but on the same frame
    assert [x["id"] for x in db.rows(VLM_STALE_SQL)] == [a]
    assert [x["id"] for x in db.rows(REVIEW_SQL)] == [b]
    db.set_local(a, {"local_tier": 3, "people": [], "exposure": {"ev": 4.0, "source": "raw"},
                     "split": {"grades": {"eye": 1, "head": 3}, "odd": "head", "gap": 2}})
    assert [x["id"] for x in db.rows(REVIEW_SQL)] == [b]   # a metrics split alone doesn't need review
    db.set_local(a, {"local_tier": 3, "people": [], "exposure": {"ev": 4.0, "source": "raw"}})

    jid = db.add_job([str(photos)], {"vlm": True})
    r.run_job(db.job(jid))
    j = db.job(jid)
    assert (j["state"], j["done"], j["total"]) == ("done", 1, 1)
    assert json.loads(db.row(a)["vlm_json"])["seen_ev"] == 4.0
    assert db.count(VLM_STALE_SQL) == 0


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



def test_new_runner_waits_for_every_job_to_be_handed_back(tmp_path, monkeypatch):
    """The old server's local-ahead lane hands its job back first; the new server must not start it in its main
    lane while the job that was with the vision model is still on its way back."""
    db = DB(tmp_path / "db.sqlite")
    main, ahead = db.add_job([], {"vlm": True}), db.add_job([], {"vlm": False})
    db.claim_job(main, "old", "main"); db.claim_job(ahead, "old", "ahead")
    new = pipeline.JobRunner(db, json.loads(json.dumps(DEFAULTS)), tmp_path, tmp_path)
    picked = []

    def run_job(job):
        picked.append(job["id"]); new.stop_event.set()
    monkeypatch.setattr(new, "run_job", run_job)
    monkeypatch.setattr(new.stop_event, "wait", lambda t=None: time.sleep(0.02) or new.stop_event.is_set())
    db.update_job(ahead, state="queued", owner=None, lane=None)   # the lane lets go first
    new.start()
    time.sleep(0.2)
    assert picked == []
    db.update_job(main, state="queued", owner=None, lane=None)
    new.join(5)
    assert picked == [main]


# ---- two lanes: local ahead while the model is busy; overrides ------------------------------------------

def _other_folder(tmp_path):
    other = tmp_path / "other"; other.mkdir()
    (other / "c.jpg").write_bytes(b"\xff\xd8\xff")
    return other


def _count_local(monkeypatch):
    calls, orig = [], local.run_local

    def counted(db_, cfg, cache, ids_paths, *a):
        calls.append([i for i, _ in ids_paths])
        return orig(db_, cfg, cache, ids_paths, *a)
    monkeypatch.setattr(local, "run_local", counted)
    return calls


def _on_first_classify(monkeypatch, hook):
    classify = backends.get().classify
    seen = []

    def wrapped(item, model, cfg):
        if not seen:
            hook()
        seen.append(item.key)
        return classify(item, model, cfg)
    monkeypatch.setattr(backends, "get", lambda *a, **k: type("B", (), {"sync": True, "default_model": "fake",
                                                                        "classify": staticmethod(wrapped)})())
    return seen


def test_local_stage_runs_ahead_while_the_model_is_busy(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    other = _other_folder(tmp_path)
    a, b = db.add_job([str(photos)], {"vlm": True}), db.add_job([str(other)], {"vlm": True})
    c = db.add_job([str(other)], {"vlm": False})
    calls = _count_local(monkeypatch)

    def wait_for_queue():   # the model is busy with a's first image until the lane has done b's and c's local
        deadline = time.time() + 10
        while db.job(c)["state"] != "done" and time.time() < deadline:
            time.sleep(0.02)
    _on_first_classify(monkeypatch, wait_for_queue)
    r.run_job(db.job(a))

    jb = db.job(b)
    assert (jb["state"], jb["lane"], jb["owner"], jb["done"], jb["total"]) == ("queued", None, None, 1, 1)
    assert jb["message"].endswith("waiting for the vision model")
    assert "finished" in json.loads(jb["stages_json"])["local"]
    assert db.job(c)["state"] == "done"                          # local-only: the lane finished it
    assert db.next_local_ahead_job() is None and db.next_queued_job()["id"] == b

    for row in db.rows("1"):
        (tmp_path / "cache" / f"{row['id']}.jpg").write_bytes(b"x")
    n_local = len(calls)
    r.run_job(db.job(b))
    jb = db.job(b)
    assert (jb["state"], jb["stage"], jb["done"], jb["total"]) == ("done", "done", 1, 1)
    assert len(calls) == n_local and db.job_item_count(b, "vlm") == 1   # local wasn't redone
    assert r.held == set()


def test_local_ahead_can_be_turned_off(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    r.cfg["local_ahead"] = False
    a, b = db.add_job([str(photos)], {"vlm": True}), db.add_job([str(_other_folder(tmp_path))], {"vlm": False})
    r.run_job(db.job(a))
    assert (db.job(b)["state"], db.job(b)["started"]) == ("queued", None)


def test_override_pauses_the_running_job_and_it_resumes_after(tmp_path, monkeypatch):
    db, r, photos = _runner(tmp_path, monkeypatch, vlm_rows_done=False)
    r.cfg["local_ahead"] = False
    a, b = db.add_job([str(photos)], {"vlm": True}), db.add_job([str(_other_folder(tmp_path))], {"vlm": False})
    seen = _on_first_classify(monkeypatch, lambda: db.override_job(b))   # clicked while a's first image is out
    r.run_job(db.job(a))
    ja = db.job(a)
    assert (ja["state"], ja["owner"], ja["done"], ja["total"]) == ("queued", None, 1, 2) and "overrode" in ja["message"]
    assert db.next_queued_job()["id"] == b

    r.run_job(db.job(b))
    assert db.job(b)["state"] == "done"
    r.run_job(db.job(a))
    ja = db.job(a)
    assert (ja["state"], ja["done"], ja["total"]) == ("done", 2, 2) and len(seen) == 2 and len(set(seen)) == 2
    assert not db.override_job(a)                                 # finished jobs can't override


def test_override_while_the_lane_holds_the_job(tmp_path):
    db = DB(tmp_path / "db.sqlite")
    main, ahead, other = db.add_job([], {}), db.add_job([], {}), db.add_job([], {})
    db.claim_job(main, "me", "main"); db.claim_job(ahead, "me", "ahead")
    assert db.override_job(ahead)
    assert db.job_lease(main) == ("preempting", "me") and db.job_lease(ahead) == ("running", "me")
    assert db.running_job() == main
    db.update_job(main, heartbeat=0.0)
    assert db.requeue_stale(pipeline.LEASE_TTL_S) == [main] and db.job_lease(main) == ("queued", None)
    db.update_job(ahead, state="queued", owner=None)
    assert db.next_queued_job()["id"] == ahead and db.job(other)["priority"] == 0

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
    # either index leading with the tier expression serves it; which one is a planner tie that varies by SQLite version
    plan = str([tuple(r) for r in plan])
    assert "USING INDEX idx_final_tier'" in plan or "USING INDEX idx_tier_lr'" in plan
    plan = db.conn.execute(f"EXPLAIN QUERY PLAN SELECT {final_tier_sql('strict')} t, COUNT(*) FROM images "
                           "WHERE local_json IS NOT NULL GROUP BY t").fetchall()
    plan = str([tuple(r) for r in plan])
    assert "idx_final_tier_strict" in plan or "idx_tier_lr_strict" in plan
