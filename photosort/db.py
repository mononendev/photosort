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
  message TEXT
);
CREATE INDEX IF NOT EXISTS idx_batch ON images(batch_id);
CREATE INDEX IF NOT EXISTS idx_folder ON images(folder);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
"""
MIGRATIONS = [
    ("images", "folder", "TEXT"), ("images", "override_json", "TEXT"),
    ("images", "local_at", "REAL"), ("images", "vlm_at", "REAL"), ("images", "lr_json", "TEXT"), ("images", "truth_json", "TEXT"),
]


class DB:
    """SQLite state. One connection per thread; writes serialized by a process-wide lock."""

    def __init__(self, path: Path):
        self.path = Path(path)
        self._local = threading.local()
        self.lock = threading.RLock()
        with self.lock:
            c = self.conn
            c.executescript(SCHEMA)
            cols = {t: {r[1] for r in c.execute(f"PRAGMA table_info({t})")} for t in ("images",)}
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
        out = {}
        for r in self.conn.execute(
            "SELECT folder, COUNT(*) n, SUM(local_json IS NOT NULL) local_done, SUM(vlm_json IS NOT NULL) vlm_done, "
            "SUM(error IS NOT NULL) errors FROM images WHERE folder = ? OR folder LIKE ? GROUP BY folder",
                (prefix, prefix.rstrip("/") + "/%")):
            out[r["folder"]] = dict(r)
        return out

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

    def clear_batch(self, batch_id: str):
        with self.lock, self.conn as c:
            c.execute("UPDATE images SET batch_id=NULL WHERE batch_id=? AND vlm_json IS NULL", (batch_id,))

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

    def update_job(self, job_id: int, **fields):
        cols = ", ".join(f"{k}=?" for k in fields)
        with self.lock, self.conn as c:
            c.execute(f"UPDATE jobs SET {cols} WHERE id=?", (*fields.values(), job_id))

    def job_state(self, job_id: int) -> Optional[str]:
        r = self.conn.execute("SELECT state FROM jobs WHERE id=?", (job_id,)).fetchone()
        return r["state"] if r else None
