"""Job runner: scan -> local -> vlm for a set of paths, with progress in the jobs table.

One worker thread runs jobs sequentially (the GPU and the local model server are shared).
"""
from __future__ import annotations
import json
import logging
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Optional

from . import images as I, schema
from .db import DB

log = logging.getLogger("photosort.pipeline")


class _Progress:
    def __init__(self, db: DB, job_id: int):
        self.db, self.job_id, self.n, self.t = db, job_id, 0, time.time()

    def update(self, k: int = 1):
        self.n += k
        if self.n % 5 == 0 or time.time() - self.t > 2:
            self.db.update_job(self.job_id, done=self.n)
            self.t = time.time()


class JobRunner(threading.Thread):
    def __init__(self, db: DB, cfg: dict, workdir: Path, photos_root: Path, device: Optional[str] = None):
        super().__init__(daemon=True, name="photosort-jobs")
        self.db, self.cfg, self.workdir, self.photos_root, self.device = db, cfg, workdir, photos_root, device
        self.cache_dir = workdir / "cache"
        self._detector = None
        self.current: Optional[int] = None
        self.stop_event = threading.Event()
        self.reload_config = threading.Event()

    # -- helpers --------------------------------------------------------------
    def _resolve(self, p: str) -> Path:
        path = Path(p)
        if not path.is_absolute():
            path = self.photos_root / path
        return path

    def _cancelled(self, job_id: int) -> bool:
        return self.stop_event.is_set() or self.db.job_state(job_id) == "cancelling"

    def detector(self):
        if self._detector is None:
            from .local import Detector
            self._detector = Detector(self.cfg["detect_model"], self.cfg["detect_long_edge"], self.cfg["detect_conf"], self.device)
        return self._detector

    # -- main loop --------------------------------------------------------------
    def run(self):
        # jobs left 'running' by a previous process are re-queued
        for j in self.db.jobs(200):
            if j["state"] in ("running", "cancelling"):
                self.db.update_job(j["id"], state="queued", stage="queued", message="requeued after restart")
        while not self.stop_event.is_set():
            job = self.db.next_queued_job()
            if job is None:
                time.sleep(2)
                continue
            self.current = job["id"]
            try:
                self.run_job(job)
            except Exception as e:
                log.exception("job %s failed", job["id"])
                self.db.update_job(job["id"], state="failed", finished=time.time(), message=f"{type(e).__name__}: {e}")
            self.current = None

    def run_job(self, job):
        jid = job["id"]
        opts = json.loads(job["options_json"] or "{}")
        paths = [self._resolve(p) for p in json.loads(job["paths_json"])]
        if self.reload_config.is_set():
            from . import config
            self.cfg.update(config.load(self.workdir))
            self.reload_config.clear()
        self.db.update_job(jid, state="running", stage="scan", started=time.time(), message=None, done=0, errors=0)

        # 1) scan
        files = []
        for p in paths:
            if p.is_file() and I.is_image(p):
                files.append(p)
            elif p.is_dir():
                files += [f for f in p.rglob("*") if f.is_file() and I.is_image(f)]
        if opts.get("skip_raw_dupes", True):
            stems = {f.with_suffix("").as_posix() for f in files if f.suffix.lower() not in I.RAW_EXT}
            files = [f for f in files if f.suffix.lower() not in I.RAW_EXT or f.with_suffix("").as_posix() not in stems]
        self.db.add_paths(files)
        if self._cancelled(jid):
            return self._finish(jid, "cancelled")

        # 2) local stage
        where_paths = " OR ".join("path = ? OR path LIKE ?" for _ in paths)
        params = [x for p in paths for x in (str(p), str(p).rstrip("/") + "/%")]
        cond = "local_json IS NULL" if not opts.get("rescan") else "1"
        rows = self.db.rows(f"({where_paths}) AND ({cond})", params) if paths else []
        self.db.update_job(jid, stage="local", total=len(rows), done=0)
        if rows:
            from .local import run_local
            prog = _Progress(self.db, jid)
            ok, err = run_local(self.db, self.cfg, self.cache_dir, [(r["id"], r["path"]) for r in rows],
                                self.device, prog, lambda: self._cancelled(jid), self.detector())
            self.db.update_job(jid, done=prog.n, errors=err)
        if self._cancelled(jid):
            return self._finish(jid, "cancelled")

        # 3) vlm stage
        if opts.get("vlm", True):
            self.run_vlm(jid, where_paths, params, opts)
            if self._cancelled(jid):
                return self._finish(jid, "cancelled")
        self._finish(jid, "done")

    def run_vlm(self, jid: int, where_paths: str, params: list, opts: dict):
        from . import backends
        from .backends import Item
        bname = opts.get("backend") or self.cfg.get("backend", "ollama")
        backend = backends.get(bname, opts.get("base_url") or self.cfg.get("base_url"))
        if not getattr(backend, "sync", False):
            self.db.update_job(jid, message=f"backend {bname} is batch-only; use the CLI submit/poll for it")
            return
        model = opts.get("model") or self.cfg.get("model") or backend.default_model
        cond = "local_json IS NOT NULL AND vlm_json IS NULL" + ("" if opts.get("retry_errors") else " AND error IS NULL")
        rows = self.db.rows(f"({where_paths}) AND {cond}", params)
        if opts.get("skip_tier0", False):
            rows = [r for r in rows if json.loads(r["local_json"])["local_tier"] > 0]
        self.db.update_job(jid, stage="vlm", total=len(rows), done=0, errors=0, message=f"{bname}/{model}")
        if not rows:
            return
        prog = _Progress(self.db, jid)
        errors = 0
        conc = int(opts.get("concurrency") or self.cfg.get("vlm_concurrency", 1))

        def one(r):
            if self._cancelled(jid):
                return None
            local = json.loads(r["local_json"])
            frame = (self.cache_dir / f"{r['id']}.jpg").read_bytes()
            cp = self.cache_dir / f"{r['id']}_crop.jpg"
            item = Item(str(r["id"]), frame, cp.read_bytes() if cp.exists() else None, schema.context_text(local))
            return backend.classify(item, model, self.cfg)

        with ThreadPoolExecutor(max_workers=conc) as ex:
            futs = [ex.submit(one, r) for r in rows]
            for f in as_completed(futs):
                res = f.result()
                if res is None:
                    continue
                if res.data:
                    self.db.set_vlm(int(res.key), res.data, res.usage, None)
                else:
                    self.db.set_vlm(int(res.key), None, res.usage, res.error)
                    errors += 1
                prog.update(1)
        self.db.update_job(jid, done=prog.n, errors=errors)

    def _finish(self, jid: int, state: str):
        self.db.update_job(jid, state=state, stage="done", finished=time.time())
