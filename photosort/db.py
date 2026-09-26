from __future__ import annotations
import json
import sqlite3
import threading
import time
from pathlib import Path
from typing import Iterable, Optional

SCHEMA = """
CREATE TABLE IF NOT EXISTS images (
  id INTEGER PRIMARY KEY,
  path TEXT UNIQUE NOT NULL,
  folder TEXT,
  size INTEGER, mtime REAL,
  local_json TEXT,           -- stage 1 result
  batch_id TEXT,             -- cloud batch this image was submitted in
  vlm_json TEXT,             -- stage 2 parsed result
  vlm_usage TEXT,            -- token usage json
  override_json TEXT,        -- manual corrections from the UI
  error TEXT,
  local_at REAL, vlm_at REAL
);
CREATE TABLE IF NOT EXISTS batches (
  id TEXT PRIMARY KEY,
  backend TEXT, model TEXT, n INTEGER,
  created REAL, state TEXT, fetched INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY,
  created REAL, started REAL, finished REAL,
  state TEXT,                -- queued | running | cancelling | done | cancelled | failed
  stage TEXT,                -- scan | local | vlm | done
  paths_json TEXT, options_json TEXT,
  total INTEGER DEFAULT 0, done INTEGER DEFAULT 0, errors INTEGER DEFAULT 0,
  message TEXT,
  owner TEXT, heartbeat REAL -- the worker running it and when it last said so (see JobRunner)
);
CREATE TABLE IF NOT EXISTS job_items (
  id INTEGER PRIMARY KEY,
  job_id INTEGER, image_id INTEGER,
  stage TEXT,                -- local | vlm
  started REAL, finished REAL, seconds REAL,   -- finished is NULL while the image is in flight
  error TEXT,
  usage_json TEXT            -- vlm token usage and timings
);
CREATE INDEX IF NOT EXISTS idx_job_items ON job_items(job_id, id);
CREATE INDEX IF NOT EXISTS idx_batch ON images(batch_id);
CREATE INDEX IF NOT EXISTS idx_folder ON images(folder);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
"""
MIGRATIONS = [
    ("images", "folder", "TEXT"), ("images", "override_json", "TEXT"),
    ("images", "local_at", "REAL"), ("images", "vlm_at", "REAL"), ("images", "lr_json", "TEXT"), ("images", "truth_json", "TEXT"),
    ("jobs", "stages_json", "TEXT"),   # per-stage timings and settings, written as the job moves through them
    ("jobs", "owner", "TEXT"), ("jobs", "heartbeat", "REAL"),
]

def jcol(row, key: str, default=None):
    """A JSON column of a row, parsed, or `default` when it is NULL/empty."""
    v = row[key]
    return json.loads(v) if v else default


# The tier a photo ends up with (your override, else the model's, else the local stage's) and whether the two
# stages disagree, as SQL over the images table. sort.final_record is the Python twin (it also knows focus_source).
FINAL_TIER_SQL = ("COALESCE(json_extract(override_json,'$.focus_tier'), json_extract(vlm_json,'$.focus_tier'), "
                  "json_extract(local_json,'$.local_tier'))")
REVIEW_SQL = ("local_json IS NOT NULL AND vlm_json IS NOT NULL AND "
              "json_extract(local_json,'$.local_tier') != json_extract(vlm_json,'$.focus_tier')")


def under_folder(folder: str, col: str = "folder") -> tuple[str, list]:
    """SQL for `col` being `folder` or anything below it. substr() rather than LIKE keeps '_' and '%' literal."""
    folder = folder.rstrip("/")
    return f"({col} = ? OR substr({col}, 1, ?) = ?)", [folder, len(folder) + 1, folder + "/"]


class DB:
    """SQLite state. One connection per thread; writes serialized by a process-wide lock."""

    def __init__(self, path: Path):
        self.path = Path(path)
        self._local = threading.local()
        self.lock = threading.RLock()
        with self.lock:
            c = self.conn
            c.executescript(SCHEMA)
            cols = {t: {r[1] for r in c.execute(f"PRAGMA table_info({t})")} for t in ("images", "jobs")}
            for table, col, typ in MIGRATIONS:
                if col not in cols[table]:
                    c.execute(f"ALTER TABLE {table} ADD COLUMN {col} {typ}")
            for r in c.execute("SELECT id, path FROM images WHERE folder IS NULL").fetchall():
                c.execute("UPDATE images SET folder=? WHERE id=?", (str(Path(r[1]).parent), r[0]))
            c.commit()

    @property
    def conn(self) -> sqlite3.Connection:
        c = getattr(self._local, "conn", None)
        if c is None:
            c = sqlite3.connect(self.path, timeout=30)
            c.row_factory = sqlite3.Row
            c.execute("PRAGMA journal_mode=WAL")
            c.execute("PRAGMA synchronous=NORMAL")
            self._local.conn = c
        return c

    # ---- images -----------------------------------------------------------
    def add_paths(self, paths: Iterable[Path]) -> int:
        n = 0
        with self.lock, self.conn as c:
            for p in paths:
                try:
                    st = p.stat()
                except OSError:
                    continue
                cur = c.execute("INSERT OR IGNORE INTO images(path,folder,size,mtime) VALUES(?,?,?,?)",
                                (str(p), str(p.parent), st.st_size, st.st_mtime))
                n += cur.rowcount
        return n

    def rows(self, where: str = "1", params=(), order: str = "path", limit: Optional[int] = None, offset: int = 0) -> list[sqlite3.Row]:
        q = f"SELECT * FROM images WHERE {where} ORDER BY {order}"
        if limit is not None:
            q += f" LIMIT {int(limit)} OFFSET {int(offset)}"
        return self.conn.execute(q, params).fetchall()

    def rows_under(self, paths: list[Path], where: str = "1", params=(), chunk: int = 400) -> list[sqlite3.Row]:
        """Rows whose path is one of `paths` or lies under one of them, AND `where`, ordered by path.

        Files go in chunked IN lists and folders in chunked prefix matches, so a job over thousands of
        selected files never builds an expression past SQLite's depth limit (1000). A prefix match with
        substr() rather than LIKE keeps '_' and '%' in folder names literal."""
        files = [str(p) for p in paths if not p.is_dir()]
        dirs = [str(p).rstrip("/") + "/" for p in paths if p.is_dir()]
        seen: dict[int, sqlite3.Row] = {}
        for i in range(0, len(files), chunk):
            part = files[i:i + chunk]
            q = f"path IN ({','.join('?' * len(part))}) AND ({where})"
            for r in self.rows(q, [*part, *params]):
                seen[r["id"]] = r
        for i in range(0, len(dirs), chunk // 4):
            part = dirs[i:i + chunk // 4]
            q = "(" + " OR ".join("substr(path, 1, ?) = ?" for _ in part) + f") AND ({where})"
            for r in self.rows(q, [x for d in part for x in (len(d), d)] + list(params)):
                seen[r["id"]] = r
        return sorted(seen.values(), key=lambda r: r["path"])

    def row(self, img_id: int) -> Optional[sqlite3.Row]:
        return self.conn.execute("SELECT * FROM images WHERE id=?", (img_id,)).fetchone()

    def count(self, where: str = "1", params=()) -> int:
        return self.conn.execute(f"SELECT COUNT(*) FROM images WHERE {where}", params).fetchone()[0]

    def set_local(self, img_id: int, data: Optional[dict], error: Optional[str] = None):
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET local_json=?, error=?, local_at=? WHERE id=?",
                      (json.dumps(data) if data else None, error, time.time() if data else None, img_id))

    def set_vlm(self, img_id: int, data: Optional[dict], usage: Optional[dict], error: Optional[str]):
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET vlm_json=?, vlm_usage=?, error=?, vlm_at=? WHERE id=?",
                      (json.dumps(data) if data else None, json.dumps(usage) if usage else None, error,
                       time.time() if data else None, img_id))

    def set_vlm_result(self, res) -> bool:
        """Store a backend Result for the image it is keyed by; says whether it carried data."""
        ok = bool(res.data)
        self.set_vlm(int(res.key), res.data if ok else None, res.usage, None if ok else res.error)
        return ok

    def set_lr(self, img_id: int, data: Optional[dict]):
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET lr_json=? WHERE id=?", (json.dumps(data) if data else "{}", img_id))

    def set_truth(self, img_id: int, data: Optional[dict]):
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET truth_json=? WHERE id=?", (json.dumps(data) if data else None, img_id))

    def clear_truth(self) -> int:
        with self.lock, self.conn as c:
            return c.execute("UPDATE images SET truth_json=NULL WHERE truth_json IS NOT NULL").rowcount

    def set_override(self, img_id: int, data: Optional[dict]):
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET override_json=? WHERE id=?", (json.dumps(data) if data else None, img_id))

    def folder_stats(self, prefix: str) -> dict[str, dict]:
        """Per-folder counts for every folder under prefix (recursive)."""
        where, params = under_folder(prefix)
        return {r["folder"]: dict(r) for r in self.conn.execute(
            "SELECT folder, COUNT(*) n, SUM(local_json IS NOT NULL) local_done, SUM(vlm_json IS NOT NULL) vlm_done, "
            f"SUM(error IS NOT NULL) errors FROM images WHERE {where} GROUP BY folder", params)}

    # ---- batches (cloud backends) ------------------------------------------
    def set_batch(self, ids: list[int], batch_id: str):
        with self.lock, self.conn as c:
            c.executemany("UPDATE images SET batch_id=? WHERE id=?", [(batch_id, i) for i in ids])

    def add_batch(self, batch_id: str, backend: str, model: str, n: int, created: float):
        with self.lock, self.conn as c:
            c.execute("INSERT OR REPLACE INTO batches VALUES(?,?,?,?,?,?,0)", (batch_id, backend, model, n, created, "submitted"))

    def batches(self, only_unfetched=False) -> list[sqlite3.Row]:
        q = "SELECT * FROM batches" + (" WHERE fetched=0" if only_unfetched else "") + " ORDER BY created"
        return self.conn.execute(q).fetchall()

    def set_batch_state(self, batch_id: str, state: str, fetched: bool = False):
        with self.lock, self.conn as c:
            c.execute("UPDATE batches SET state=?, fetched=? WHERE id=?", (state, int(fetched), batch_id))

    def clear_batch(self, batch_id: str, errored_too: bool = True):
        """Release a batch's untagged images for resubmission (with errored_too=False, only ones with no error)."""
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET batch_id=NULL WHERE batch_id=? AND vlm_json IS NULL"
                      + ("" if errored_too else " AND error IS NULL"), (batch_id,))

    # ---- jobs ---------------------------------------------------------------
    def add_job(self, paths: list[str], options: dict) -> int:
        with self.lock, self.conn as c:
            cur = c.execute("INSERT INTO jobs(created,state,stage,paths_json,options_json) VALUES(?,?,?,?,?)",
                            (time.time(), "queued", "queued", json.dumps(paths), json.dumps(options)))
            return cur.lastrowid

    def jobs(self, limit: int = 50) -> list[sqlite3.Row]:
        return self.conn.execute("SELECT * FROM jobs ORDER BY id DESC LIMIT ?", (limit,)).fetchall()

    def job(self, job_id: int) -> Optional[sqlite3.Row]:
        return self.conn.execute("SELECT * FROM jobs WHERE id=?", (job_id,)).fetchone()

    def next_queued_job(self) -> Optional[sqlite3.Row]:
        return self.conn.execute("SELECT * FROM jobs WHERE state='queued' ORDER BY id LIMIT 1").fetchone()

    def update_job(self, job_id: int, where_owner: Optional[str] = None, **fields) -> bool:
        """With where_owner, only writes while that worker still holds the job, and says whether it did."""
        cols = ", ".join(f"{k}=?" for k in fields)
        q, params = f"UPDATE jobs SET {cols} WHERE id=?", [*fields.values(), job_id]
        if where_owner is not None:
            q += " AND owner=?"; params.append(where_owner)
        with self.lock, self.conn as c:
            return c.execute(q, params).rowcount > 0

    # A worker holds a job while it keeps the heartbeat fresh. During a rolling restart two workers share this
    # database: the new one leaves a job alone while the old one is still beating, and picks it up once the old
    # one hands it back (clean shutdown) or its heartbeat goes stale (killed).
    def claim_job(self, job_id: int, owner: str) -> bool:
        with self.lock, self.conn as c:
            ok = c.execute("UPDATE jobs SET state='running', owner=?, heartbeat=? WHERE id=? AND state='queued'",
                           (owner, time.time(), job_id)).rowcount > 0
            if ok:  # images the previous worker had in flight never finished
                c.execute("DELETE FROM job_items WHERE job_id=? AND finished IS NULL", (job_id,))
            return ok

    def heartbeat(self, job_id: int, owner: str) -> bool:
        return self.update_job(job_id, where_owner=owner, heartbeat=time.time())

    def job_lease(self, job_id: int) -> tuple[Optional[str], Optional[str]]:
        r = self.conn.execute("SELECT state, owner FROM jobs WHERE id=?", (job_id,)).fetchone()
        return (r["state"], r["owner"]) if r else (None, None)

    def requeue_stale(self, ttl: float) -> list[int]:
        """Running jobs whose worker went quiet go back in the queue; ones being cancelled end as cancelled."""
        cutoff, now = time.time() - ttl, time.time()
        with self.lock, self.conn as c:
            stale = [r["id"] for r in c.execute(
                "SELECT id FROM jobs WHERE state IN ('running','cancelling') AND (heartbeat IS NULL OR heartbeat < ?)", (cutoff,))]
            for jid in stale:
                c.execute("UPDATE jobs SET state = CASE state WHEN 'cancelling' THEN 'cancelled' ELSE 'queued' END, "
                          "stage = CASE state WHEN 'cancelling' THEN 'done' ELSE 'queued' END, "
                          "finished = CASE state WHEN 'cancelling' THEN ? ELSE finished END, owner=NULL, heartbeat=NULL, "
                          "message = CASE state WHEN 'cancelling' THEN message ELSE 'requeued: its worker stopped responding' END "
                          "WHERE id=?", (now, jid))
                c.execute("DELETE FROM job_items WHERE job_id=? AND finished IS NULL", (jid,))
            return stale

    def cancel_job(self, job_id: int):
        with self.lock, self.conn as c:
            c.execute("UPDATE jobs SET state = CASE state WHEN 'queued' THEN 'cancelled' ELSE 'cancelling' END, "
                      "finished = CASE state WHEN 'queued' THEN ? ELSE finished END "
                      "WHERE id=? AND state IN ('queued','running')", (time.time(), job_id))

    def running_job(self) -> Optional[int]:
        r = self.conn.execute("SELECT id FROM jobs WHERE state IN ('running','cancelling') ORDER BY id LIMIT 1").fetchone()
        return r["id"] if r else None

    def start_job_item(self, job_id: int, image_id: int, stage: str, started: float) -> int:
        with self.lock, self.conn as c:
            return c.execute("INSERT INTO job_items(job_id,image_id,stage,started) VALUES(?,?,?,?)",
                             (job_id, image_id, stage, started)).lastrowid

    def finish_job_item(self, item_id: int, finished: float, error: Optional[str] = None, usage: Optional[dict] = None):
        with self.lock, self.conn as c:
            c.execute("UPDATE job_items SET finished=?, seconds=round(? - started, 3), error=?, usage_json=? WHERE id=?",
                      (finished, finished, error, json.dumps(usage) if usage else None, item_id))

    def add_job_item(self, job_id: int, image_id: int, stage: str, started: float, finished: float,
                     error: Optional[str] = None, usage: Optional[dict] = None):
        with self.lock, self.conn as c:
            c.execute("INSERT INTO job_items(job_id,image_id,stage,started,finished,seconds,error,usage_json) VALUES(?,?,?,?,?,?,?,?)",
                      (job_id, image_id, stage, started, finished, round(finished - started, 3), error,
                       json.dumps(usage) if usage else None))

    def in_flight(self, job_id: int) -> list[sqlite3.Row]:
        return self.conn.execute(
            "SELECT ji.image_id AS id, ji.stage, ji.started, i.path FROM job_items ji LEFT JOIN images i ON i.id = ji.image_id "
            "WHERE ji.job_id = ? AND ji.finished IS NULL ORDER BY ji.started", (job_id,)).fetchall()

    def job_finished_images(self, job_id: int, stage: str) -> tuple[set[int], int]:
        """Images this job already got through in a stage (by an earlier worker, when resuming), and how many failed."""
        rows = self.conn.execute("SELECT image_id, error IS NOT NULL AS failed FROM job_items "
                                 "WHERE job_id = ? AND stage = ? AND finished IS NOT NULL", (job_id, stage)).fetchall()
        return {r["image_id"] for r in rows}, sum(r["failed"] for r in rows)

    def job_items(self, job_id: int, stage: Optional[str] = None, errors: bool = False,
                  limit: int = 50, offset: int = 0) -> list[sqlite3.Row]:
        """Newest first, joined with the image's path and current results."""
        where, params = ["ji.job_id = ?", "ji.finished IS NOT NULL"], [job_id]
        if stage:
            where.append("ji.stage = ?"); params.append(stage)
        if errors:
            where.append("ji.error IS NOT NULL")
        return self.conn.execute(
            "SELECT ji.*, i.path, i.local_json, i.vlm_json, i.override_json FROM job_items ji "
            f"LEFT JOIN images i ON i.id = ji.image_id WHERE {' AND '.join(where)} ORDER BY ji.finished DESC LIMIT ? OFFSET ?",
            (*params, int(limit), int(offset))).fetchall()

    def job_item_count(self, job_id: int, stage: Optional[str] = None, errors: bool = False) -> int:
        q = "SELECT COUNT(*) FROM job_items WHERE job_id = ? AND finished IS NOT NULL" + (" AND stage = ?" if stage else "") + (" AND error IS NOT NULL" if errors else "")
        return self.conn.execute(q, (job_id, stage) if stage else (job_id,)).fetchone()[0]

    def job_item_timings(self, job_id: int) -> list[sqlite3.Row]:
        """Every item's stage, finish time, duration, error flag and usage, oldest first, for stats and charts."""
        return self.conn.execute("SELECT stage, started, finished, seconds, error IS NOT NULL AS failed, usage_json "
                                 "FROM job_items WHERE job_id = ? AND finished IS NOT NULL ORDER BY finished", (job_id,)).fetchall()

