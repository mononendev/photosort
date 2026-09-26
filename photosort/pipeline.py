"""Job runner: scan -> local -> vlm for a set of paths, with progress in the jobs table.

One worker thread runs jobs sequentially (the GPU and the local model server are shared).

Rolling restarts: for a while the old and new server share the database, each with a runner. A runner
claims a job and holds it with a heartbeat; the other leaves it alone until it is handed back (the old
server stopping cleanly) or the heartbeat goes stale (killed). Every write to the job row is fenced on
the claim, so a runner that lost its job can't overwrite the new one's progress. A job picked up again
resumes: images it already finished count towards done/total and aren't redone.
"""
from __future__ import annotations
import json
import logging
import os
import socket
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Optional

from . import images as I
from .db import DB

log = logging.getLogger("photosort.pipeline")


HEARTBEAT_S = 5       # how often the runner renews its claim on the job it is running
LEASE_TTL_S = 45      # a claim this old belongs to a dead worker; its job goes back in the queue
DRAIN_S = 20          # on shutdown, how long in-flight images get to finish (k8s grace period is 30 s)


class _Progress:
    """Counts finished images for the job row, and keeps the per-image record behind the job detail view:
    start() adds an in-flight job item, finish() stamps it with timing, error and token usage. Both live in
    the database, so whichever server answers the UI sees the same in-flight set."""

    def __init__(self, db: DB, job_id: int, stage: str = "", owner: Optional[str] = None, done: int = 0):
        self.db, self.job_id, self.stage, self.owner, self.n, self.t = db, job_id, stage, owner, done, time.time()
        self.items: dict[int, int] = {}          # image id -> its in-flight job item
        self.lock = threading.Lock()

    def update(self, k: int = 1):
        self.n += k
        if self.n % 5 == 0 or time.time() - self.t > 2:
            self.db.update_job(self.job_id, where_owner=self.owner, done=self.n)
            self.t = time.time()

    def start(self, img_id: int, path: str):
        item = self.db.start_job_item(self.job_id, img_id, self.stage, time.time())
        with self.lock:
            self.items[img_id] = item

    def finish(self, img_id: int, error: Optional[str] = None, usage: Optional[dict] = None):
        with self.lock:
            item = self.items.pop(img_id, None)
        now = time.time()
        if item is None:
            self.db.add_job_item(self.job_id, img_id, self.stage, now, now, error, usage)
        else:
            self.db.finish_job_item(item, now, error, usage)


class JobRunner(threading.Thread):
    def __init__(self, db: DB, cfg: dict, workdir: Path, photos_root: Path, device: Optional[str] = None):
        super().__init__(daemon=True, name="photosort-jobs")
        self.db, self.cfg, self.workdir, self.photos_root, self.device = db, cfg, workdir, photos_root, device
        self.cache_dir = workdir / "cache"
        self._detector = None
        self.current: Optional[int] = None
        self.owner = f"{socket.gethostname()}/{os.getpid()}/{uuid.uuid4().hex[:6]}"
        self._stages: dict[str, dict] = {}      # the current job's stages_json
        self.stop_event = threading.Event()
        self.reload_config = threading.Event()

    # -- helpers --------------------------------------------------------------
    def _resolve(self, p: str) -> Path:
        path = Path(p)
        if not path.is_absolute():
            path = self.photos_root / path
        return path

    def _job(self, jid: int, **fields) -> bool:
        """Write to the job row only while this runner still holds it."""
        return self.db.update_job(jid, where_owner=self.owner, **fields)

    def _cancelled(self, job_id: int) -> bool:
        """Stop early: the user cancelled, this server is shutting down, or another runner took the job over."""
        return self.stop_event.is_set() or self.db.job_lease(job_id) != ("running", self.owner)

    def _progress(self, jid: int, stage: str, done: int = 0) -> _Progress:
        return _Progress(self.db, jid, stage, self.owner, done)

    def _stage(self, jid: int, name: str, **info):
        """Record a stage starting (first call) or its settings/outcome (later calls) in stages_json.
        Starting a stage closes the one before it."""
        now = time.time()
        if name not in self._stages:
            for st in self._stages.values():
                st.setdefault("finished", now)
            self._stages[name] = {"started": now}
        self._stages[name].update(info)
        self._job(jid, stages_json=json.dumps(self._stages))

    def detector(self):
        if self._detector is None:
            from .local import Detector
            self._detector = Detector(self.cfg["detect_model"], self.cfg["detect_long_edge"], self.cfg["detect_conf"], self.device)
        return self._detector

    # -- main loop --------------------------------------------------------------
    def run(self):
        threading.Thread(target=self._heartbeat, daemon=True, name="photosort-lease").start()
        while not self.stop_event.is_set():
            for jid in self.db.requeue_stale(LEASE_TTL_S):
                log.warning("job %s: its worker stopped responding; requeued", jid)
            job = self.db.next_queued_job()
            if job is None:
                self.stop_event.wait(2)
                continue
            self.current = job["id"]
            try:
                self.run_job(job)
            except Exception as e:
                log.exception("job %s failed", job["id"])
                self._job(job["id"], state="failed", finished=time.time(), owner=None, heartbeat=None,
                          message=f"{type(e).__name__}: {e}")
            self.current = None

    def _heartbeat(self):
        while not self.stop_event.wait(HEARTBEAT_S):
            jid = self.current
            if jid is not None:
                self.db.heartbeat(jid, self.owner)

    def shutdown(self, timeout: float = DRAIN_S):
        """The server is stopping (e.g. SIGTERM in a rolling restart): take no new images, give the in-flight
        ones up to `timeout` to finish, and put the job back in the queue so the next server resumes it now
        rather than after the claim goes stale. Anything still running after that is fenced out."""
        self.stop_event.set()
        if self.is_alive():
            self.join(timeout)
        jid = self.current
        if jid is not None and self._release(jid, save_stages=False):
            log.info("job %s: still busy after %.0fs; handed back anyway", jid, timeout)

    def _stopped(self, jid: int):
        state, owner = self.db.job_lease(jid)
        if owner != self.owner:
            return  # another runner has it now; leave its progress alone
        if state == "cancelling":
            return self._finish(jid, "cancelled")
        self._release(jid)

    def _release(self, jid: int, save_stages: bool = True) -> bool:
        extra = {"stages_json": json.dumps(self._stages)} if save_stages else {}
        return self._job(jid, state="queued", stage="queued", owner=None, heartbeat=None,
                         message="handed back to the queue: server restarting", **extra)

    def run_job(self, job):
        jid = job["id"]
        if not self.db.claim_job(jid, self.owner):
            return  # another runner got it first
        opts = json.loads(job["options_json"] or "{}")
        paths = [self._resolve(p) for p in json.loads(job["paths_json"])]
        if self.reload_config.is_set():
            from . import config
            self.cfg.update(config.load(self.workdir))
            self.reload_config.clear()
        # a job that was handed back or requeued keeps its start time, stage history and finished images
        resumed = job["started"] is not None
        self._stages = json.loads(job["stages_json"] or "{}") if resumed else {}
        if resumed:
            self._job(jid, stage="scan", message="resumed after restart")
        else:
            self._job(jid, stage="scan", started=time.time(), message=None, done=0, errors=0)
        self._stage(jid, "scan")

        # 1) scan
        files = I.find_images(paths, opts.get("skip_raw_dupes", True))
        self.db.add_paths(files)
        from . import sidecar
        if paths:
            sidecar.ingest(self.db, self.db.rows_under(paths, "lr_json IS NULL"))
        self._stage(jid, "scan", files=len(files))
        if self._cancelled(jid):
            return self._stopped(jid)

        # 2) local stage
        cond = "local_json IS NULL" if not opts.get("rescan") else "1"
        prior, local_err = self.db.job_finished_images(jid, "local")
        rows = [r for r in self.db.rows_under(paths, cond) if r["id"] not in prior] if paths else []
        total = len(prior) + len(rows)
        self._job(jid, stage="local", total=total, done=len(prior), errors=local_err)
        self._stage(jid, "local", total=total, workers=self.cfg.get("workers"))
        local_note = f"local {len(prior)}/{total}" if prior else "local: nothing new"
        if rows:
            from .local import run_local
            prog = self._progress(jid, "local", len(prior))
            ok, n_err = run_local(self.db, self.cfg, self.cache_dir, [(r["id"], r["path"]) for r in rows],
                                  self.device, prog, lambda: self._cancelled(jid), self.detector())
            local_err += n_err
            self._job(jid, done=prog.n, errors=local_err)
            local_note = f"local {prog.n}/{total}"
            self._stage(jid, "local", done=prog.n, errors=local_err,
                        device=getattr(self._detector, "device", None) or self.device)
        self._job(jid, message=local_note)
        if self._cancelled(jid):
            return self._stopped(jid)

        # 3) vlm stage
        if opts.get("vlm", True):
            self.run_vlm(jid, paths, opts, local_err, local_note)
            if self._cancelled(jid):
                return self._stopped(jid)
        self._finish(jid, "done")

    def run_vlm(self, jid: int, paths: list[Path], opts: dict, local_err: int = 0, local_note: str = ""):
        """Counters switch to the vlm stage only when it has work, so a local-only re-analysis keeps its
        own done/total; errors from both stages add up. The message keeps the local stage's summary."""
        from . import backends
        bname = opts.get("backend") or self.cfg.get("backend", "ollama")
        backend = backends.get(bname, opts.get("base_url") or self.cfg.get("base_url"))
        if not getattr(backend, "sync", False):
            self._job(jid, message=f"backend {bname} is batch-only; use the CLI submit/poll for it")
            return
        model = opts.get("model") or self.cfg.get("model") or backend.default_model
        conc = int(opts.get("concurrency") or self.cfg.get("vlm_concurrency", 1))
        cond = ("local_json IS NOT NULL" + ("" if opts.get("revlm") else " AND vlm_json IS NULL")
                + ("" if opts.get("retry_errors") else " AND error IS NULL"))
        prior, errors = self.db.job_finished_images(jid, "vlm")
        rows = [r for r in self.db.rows_under(paths, cond) if r["id"] not in prior] if paths else []
        if opts.get("skip_tier0", False):
            rows = [r for r in rows if json.loads(r["local_json"])["local_tier"] > 0]
        prefix = f"{local_note} · " if local_note else ""
        if not rows and not prior:
            self._job(jid, message=f"{prefix}{bname}/{model}: nothing new to tag")
            return
        total = len(prior) + len(rows)
        self._job(jid, stage="vlm", total=total, done=len(prior), errors=local_err + errors, message=f"{prefix}{bname}/{model}")
        self._stage(jid, "vlm", total=total, backend=bname, model=model, concurrency=conc,
                    base_url=getattr(backend, "base_url", None))
        prog = self._progress(jid, "vlm", len(prior))

        def one(r):
            if self._cancelled(jid):
                return None
            prog.start(r["id"], r["path"])
            try:
                return backend.classify(backends.load_item(r, self.cache_dir), model, self.cfg)
            except Exception as e:
                prog.finish(r["id"], f"{type(e).__name__}: {e}")
                raise

        with ThreadPoolExecutor(max_workers=conc) as ex:
            futs = [ex.submit(one, r) for r in rows]
            for f in as_completed(futs):
                res = f.result()
                if res is None:
                    continue
                if not self.db.set_vlm_result(res):
                    errors += 1
                prog.finish(int(res.key), res.error, res.usage)
                prog.update(1)
        self._job(jid, done=prog.n, errors=local_err + errors)
        self._stage(jid, "vlm", done=prog.n, errors=errors)

    def _finish(self, jid: int, state: str):
        now = time.time()
        for st in self._stages.values():
            st.setdefault("finished", now)
        self._job(jid, state=state, stage="done", finished=now, owner=None, heartbeat=None,
                  stages_json=json.dumps(self._stages))
