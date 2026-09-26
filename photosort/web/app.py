"""FastAPI backend for the photosort UI. Static assets are served by the UI container (nginx)."""
import json
import os
import time
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, File, HTTPException, Query, UploadFile
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

from .. import __version__, config, images as I, schema, sort as sorter, truth
from ..db import DB, FINAL_TIER_SQL, jcol, REVIEW_SQL, under_folder
from ..pipeline import JobRunner


class JobIn(BaseModel):
    paths: list[str]
    vlm: bool = True
    skip_tier0: bool = False
    rescan: bool = False
    retry_errors: bool = False
    concurrency: int = 1
    model: Optional[str] = None


class OverrideIn(BaseModel):
    rating: Optional[int] = Field(None, ge=0, le=4)   # your cull: 0-3 focus tier, 4 banger; marks the photo reviewed
    focus_tier: Optional[int] = Field(None, ge=0, le=3)
    quality_score: Optional[int] = None
    keeper: Optional[bool] = None
    note: Optional[str] = None
    clear: bool = False


class ConfigIn(BaseModel):
    values: dict


class TruthImportIn(BaseModel):
    dir: str                      # folder on the photos or data volume containing .xmp/.xml/.csv/.zip
    folder: str = ""              # only match images under this photos subfolder


class ExportIn(BaseModel):
    name: str = "export"
    folder: str = ""
    link: str = "copy"           # copy | symlink | hardlink (photos are read-only, so no move)
    xmp: bool = True
    focus_source: Optional[str] = None
    tree: bool = True


def create_app(workdir: Path, photos_root: Path, device: Optional[str] = None) -> FastAPI:
    workdir.mkdir(parents=True, exist_ok=True)
    cfg = config.load(workdir)
    if os.environ.get("OLLAMA_HOST"):        # deployment wiring wins over a stale config.json
        cfg["base_url"] = os.environ["OLLAMA_HOST"]
        cfg["backend"] = "ollama"
    db = DB(workdir / "photosort.db")
    runner = JobRunner(db, cfg, workdir, photos_root, device)
    cache = workdir / "cache"
    app = FastAPI(title="photosort", version=__version__)
    app.state.db, app.state.runner = db, runner

    @app.on_event("startup")
    def _start():
        runner.start()

    @app.on_event("shutdown")
    def _stop():
        runner.shutdown()

    # ---- helpers ------------------------------------------------------------
    def rel(p: str) -> str:
        try:
            r = str(Path(p).relative_to(photos_root))
            return "" if r == "." else r
        except ValueError:
            return p

    def safe_path(p: str) -> Path:
        path = (photos_root / p).resolve() if not Path(p).is_absolute() else Path(p).resolve()
        if photos_root.resolve() not in (path, *path.parents):
            raise HTTPException(400, "path outside photos root")
        return path

    def summary(row) -> dict:
        rec = sorter.final_record(row, cfg.get("focus_source", "vlm"))
        local = jcol(row, "local_json")
        lr = jcol(row, "lr_json", {})
        tr = jcol(row, "truth_json", {})
        return {
            "id": row["id"], "path": row["path"], "rel": rel(row["path"]), "name": Path(row["path"]).name,
            "folder": rel(row["folder"] or str(Path(row["path"]).parent)),
            "status": "tagged" if row["vlm_json"] else ("analyzed" if row["local_json"] else ("error" if row["error"] else "pending")),
            "has_crop": bool(local and local.get("n_people")),
            "focus_tier": rec["focus_tier"], "focus_tier_local": rec["focus_tier_local"], "focus_tier_vlm": rec["focus_tier_vlm"],
            "review": rec["review"], "subject": rec["subject"], "composition": rec["composition"],
            "quality_score": rec["quality_score"], "keeper": rec["keeper"], "overridden": rec["overridden"],
            "rating": rec["rating"], "reviewed": rec["reviewed"],
            "people_count": rec["people_count"], "description": rec["description"], "error": row["error"],
            "lr_rating": lr.get("rating"), "lr_label": lr.get("label"),
            "truth_tier": tr.get("focus_tier"), "truth_rating": tr.get("rating"), "truth_label": tr.get("label"),
        }

    # ---- health / stats -----------------------------------------------------
    @app.get("/api/health")
    def health():
        dev = runner._detector.device if runner._detector else None
        return {"ok": True, "version": __version__, "photos_root": str(photos_root), "workdir": str(workdir),
                "models_dir": str(config.models_dir()),
                "device": dev, "backend": cfg.get("backend"), "ollama": cfg.get("base_url"), "current_job": db.running_job()}

    KEEPER_SQL = "COALESCE(json_extract(override_json,'$.keeper'), json_extract(vlm_json,'$.keeper'))"

    @app.get("/api/stats")
    def stats():
        c = db.conn
        r = c.execute(
            "SELECT COUNT(*) tracked, COUNT(*) FILTER (WHERE local_json IS NOT NULL) analyzed, "
            "COUNT(*) FILTER (WHERE vlm_json IS NOT NULL) tagged, COUNT(*) FILTER (WHERE error IS NOT NULL) errors, "
            f"COUNT(*) FILTER (WHERE {REVIEW_SQL}) review, COUNT(*) FILTER (WHERE {KEEPER_SQL} = 1) keepers, "
            "COUNT(*) FILTER (WHERE json_extract(lr_json,'$.rating') > 0) lr_rated FROM images").fetchone()
        tiers = {f"tier{k}": 0 for k in (0, 1, 2, 3)}
        for t in c.execute(f"SELECT {FINAL_TIER_SQL} t, COUNT(*) n FROM images WHERE local_json IS NOT NULL GROUP BY t"):
            if t["t"] is not None:
                tiers[f"tier{int(t['t'])}"] += t["n"]
        return {**dict(r), "tiers": tiers,
                "lr_by_tier": [dict(x) for x in c.execute(
                    f"SELECT {FINAL_TIER_SQL} tier, json_extract(lr_json,'$.rating') rating, COUNT(*) n FROM images "
                    "WHERE local_json IS NOT NULL AND json_extract(lr_json,'$.rating') IS NOT NULL "
                    "GROUP BY tier, rating ORDER BY tier, rating")]}

    # ---- browse -----------------------------------------------------------------
    @app.get("/api/tree")
    def tree(path: str = ""):
        base = safe_path(path)
        if not base.is_dir():
            raise HTTPException(404, "not a directory")
        # Every tracked folder under base, rolled up into the child of base it sits in.
        agg: dict[str, dict] = {}
        for folder, st in db.folder_stats(str(base)).items():
            child = Path(folder).relative_to(base).parts[:1]
            if child:
                a = agg.setdefault(child[0], {"tracked": 0, "local_done": 0, "vlm_done": 0, "errors": 0})
                a["tracked"] += st["n"]; a["local_done"] += st["local_done"] or 0
                a["vlm_done"] += st["vlm_done"] or 0; a["errors"] += st["errors"] or 0
        here = {r["path"]: r for r in db.rows("folder = ?", [str(base)])}
        dirs, files = [], []
        try:
            entries = sorted(os.scandir(base), key=lambda e: e.name.lower())
        except PermissionError:
            raise HTTPException(403, "permission denied")
        for e in entries:
            if e.name.startswith("."):
                continue
            if e.is_dir(follow_symlinks=False):
                try:
                    n_direct = sum(1 for x in os.scandir(e.path) if x.is_file() and I.is_image(Path(x.name)))
                except PermissionError:
                    n_direct = 0
                dirs.append({"name": e.name, "path": rel(e.path), "images_direct": n_direct,
                             **agg.get(e.name, {"tracked": 0, "local_done": 0, "vlm_done": 0, "errors": 0})})
            elif e.is_file() and I.is_image(Path(e.name)):
                row = here.get(e.path)
                files.append(summary(row) if row else {"id": None, "name": e.name, "rel": rel(e.path), "path": e.path, "status": "untracked"})
        return {"path": rel(str(base)) if base != photos_root else "", "dirs": dirs, "files": files}

    # ---- jobs -------------------------------------------------------------------
    def job_out(j) -> dict:
        d = dict(j)
        d["paths"] = json.loads(d.pop("paths_json") or "[]")
        d["options"] = json.loads(d.pop("options_json") or "{}")
        d["stages"] = json.loads(d.pop("stages_json", None) or "{}")
        if d["started"] and d["done"] and d["state"] == "running":
            # done/total count the current stage, so time it from that stage's start, not the job's
            el = time.time() - (d["stages"].get(d["stage"], {}).get("started") or d["started"])
            d["rate"] = round(d["done"] / el, 2) if el else None
            d["eta_s"] = round((d["total"] - d["done"]) / d["rate"]) if d.get("rate") else None
        return d

    @app.post("/api/jobs")
    def create_job(job: JobIn):
        if not job.paths:
            raise HTTPException(400, "no paths")
        paths = [str(safe_path(p)) for p in job.paths]
        jid = db.add_job(paths, job.model_dump(exclude={"paths"}))
        return job_out(db.job(jid))

    @app.get("/api/jobs")
    def list_jobs(limit: int = 50):
        return [job_out(j) for j in db.jobs(limit)]

    @app.get("/api/jobs/{job_id}")
    def get_job(job_id: int):
        j = db.job(job_id)
        if not j:
            raise HTTPException(404)
        return job_out(j)

    def stage_stats(rows: list) -> dict:
        """Timing and token figures for one stage's finished items (oldest first)."""
        import numpy as np
        secs = np.array([r["seconds"] for r in rows if r["seconds"] is not None and not r["failed"]] or [0.0])
        us = [json.loads(r["usage_json"]) for r in rows if r["usage_json"]]
        tin = sum(u.get("in") or 0 for u in us)
        tout = sum(u.get("out") or 0 for u in us)
        decode = sum(u.get("decode_s") or 0 for u in us)
        recent = us[-10:]
        rdec = sum(u.get("decode_s") or 0 for u in recent)
        span = rows[-1]["finished"] - rows[0]["started"] if rows else 0
        last = rows[-20:]
        lspan = last[-1]["finished"] - last[0]["finished"] if len(last) > 1 else 0
        with_prefill = [u["prefill_s"] for u in us if u.get("prefill_s") is not None]
        return {
            "n": len(rows), "errors": sum(1 for r in rows if r["failed"]),
            "avg_s": round(float(secs.mean()), 2), "p50_s": round(float(np.percentile(secs, 50)), 2),
            "p95_s": round(float(np.percentile(secs, 95)), 2), "max_s": round(float(secs.max()), 2),
            "rate": round(len(rows) / span, 3) if span > 0 else None,                      # images/s over the stage
            "recent_rate": round((len(last) - 1) / lspan, 3) if lspan > 0 else None,      # images/s over the last 20
            "tokens_in": tin, "tokens_out": tout,
            "tok_s": round(tout / decode, 1) if decode else None,                         # decode tokens/s, whole stage
            "recent_tok_s": round(sum(u.get("out") or 0 for u in recent) / rdec, 1) if rdec else None,
            "avg_in": round(tin / len(us)) if us else None, "avg_out": round(tout / len(us)) if us else None,
            "avg_prefill_s": round(sum(with_prefill) / len(with_prefill), 2) if with_prefill else None,
            "avg_decode_s": round(decode / len(us), 2) if us else None,
        }

    @app.get("/api/jobs/{job_id}/detail")
    def job_detail(job_id: int, points: int = 300):
        """Everything the job page shows except the item list: stage timings, per-stage throughput and tokens,
        the images in flight right now, and a per-image series for the charts."""
        j = db.job(job_id)
        if not j:
            raise HTTPException(404)
        out = job_out(j)
        rows = db.job_item_timings(job_id)
        by_stage: dict[str, list] = {}
        for r in rows:
            by_stage.setdefault(r["stage"], []).append(r)
        out["stats"] = {st: stage_stats(rs) for st, rs in by_stage.items()}
        series = []   # the last `points` images of each stage, so a finished stage keeps its charts
        for r in (r for rs in by_stage.values() for r in rs[-points:]):
            u = jcol(r, "usage_json", {})
            series.append({"t": r["finished"], "stage": r["stage"], "s": r["seconds"], "err": bool(r["failed"]),
                           "tok_s": u.get("tok_s"), "out": u.get("out")})
        out["series"] = series
        now = out["now"] = time.time()
        active = [dict(a) for a in db.in_flight(job_id)] if out["state"] in ("running", "cancelling") else []
        out["active"] = [{**a, "name": Path(a["path"]).name, "rel": rel(a["path"]), "elapsed": round(now - a["started"], 1),
                          "has_thumb": (cache / f"{a['id']}_thumb.jpg").exists()} for a in active]
        out["runner"] = {"device": runner._detector.device if runner._detector else None,
                         "backend": cfg.get("backend"), "model": cfg.get("model"), "base_url": cfg.get("base_url"),
                         "workers": cfg.get("workers"), "vlm_concurrency": cfg.get("vlm_concurrency")}
        return out

    def item_out(r) -> dict:
        local = jcol(r, "local_json")
        vlm = jcol(r, "vlm_json")
        d = {"id": r["id"], "image_id": r["image_id"], "stage": r["stage"], "started": r["started"],
             "finished": r["finished"], "seconds": r["seconds"], "error": r["error"],
             "usage": jcol(r, "usage_json"),
             "name": Path(r["path"]).name if r["path"] else None, "rel": rel(r["path"]) if r["path"] else None,
             "has_crop": bool(local and local.get("n_people"))}
        if local:
            d["local"] = {k: local.get(k) for k in ("local_tier", "local_reason", "n_people", "primary_eye_sharp",
                                                    "primary_eye_hf", "primary_head_sharp", "primary_by")}
        if vlm:
            d["vlm"] = {k: vlm.get(k) for k in ("focus_tier", "primary_subject", "composition", "quality_score",
                                                "keeper", "description", "keywords")}
        return d

    @app.get("/api/jobs/{job_id}/items")
    def job_items(job_id: int, stage: Optional[str] = None, errors: bool = False, offset: int = 0,
                  limit: int = Query(50, le=500)):
        if not db.job(job_id):
            raise HTTPException(404)
        return {"total": db.job_item_count(job_id, stage, errors), "offset": offset,
                "items": [item_out(r) for r in db.job_items(job_id, stage, errors, limit, offset)]}

    @app.post("/api/jobs/{job_id}/cancel")
    def cancel_job(job_id: int):
        j = db.job(job_id)
        if not j:
            raise HTTPException(404)
        db.cancel_job(job_id)
        return job_out(db.job(job_id))

    # ---- images -------------------------------------------------------------------
    @app.get("/api/images")
    def list_images(folder: str = "", recursive: bool = True, tier: Optional[int] = None, keeper: Optional[bool] = None,
                    subject: Optional[str] = None, status: Optional[str] = None, review: Optional[bool] = None,
                    lr_rating: Optional[int] = None, lr_label: Optional[str] = None,
                    truth_tier: Optional[int] = None, truth_mismatch: Optional[bool] = None,
                    rating: Optional[int] = None, reviewed: Optional[bool] = None,
                    q: Optional[str] = None, sort: str = "path", offset: int = 0, limit: int = Query(60, le=500)):
        where, params = ["1"], []
        if folder:
            base = str(safe_path(folder))
            if recursive:
                w, p = under_folder(base)
                where.append(w); params += p
            else:
                where.append("folder = ?"); params.append(base)
        if status == "pending":
            where.append("local_json IS NULL")
        elif status == "analyzed":
            where.append("local_json IS NOT NULL AND vlm_json IS NULL")
        elif status == "tagged":
            where.append("vlm_json IS NOT NULL")
        elif status == "error":
            where.append("error IS NOT NULL")
        if tier is not None:
            where.append(f"{FINAL_TIER_SQL} = ?")
            params.append(tier)
        if keeper is not None:
            where.append(f"{KEEPER_SQL} = ?"); params.append(int(keeper))
        if subject:
            where.append("json_extract(vlm_json,'$.primary_subject') = ?"); params.append(subject)
        if review:
            where.append(REVIEW_SQL)
        if lr_rating is not None:
            where.append("json_extract(lr_json,'$.rating') = ?"); params.append(lr_rating)
        if lr_label:
            where.append("json_extract(lr_json,'$.label') = ?"); params.append(lr_label)
        if truth_tier is not None:
            where.append("json_extract(truth_json,'$.focus_tier') = ?"); params.append(truth_tier)
        if truth_mismatch:
            where.append("json_extract(truth_json,'$.focus_tier') IS NOT NULL AND local_json IS NOT NULL AND "
                         f"json_extract(truth_json,'$.focus_tier') != {FINAL_TIER_SQL}")
        if rating is not None:
            where.append("json_extract(override_json,'$.rating') = ?"); params.append(rating)
        if reviewed is not None:
            where.append("COALESCE(json_extract(override_json,'$.reviewed'), 0) = ?"); params.append(int(reviewed))
        if q:
            where.append("(path LIKE ? OR vlm_json LIKE ?)"); params += [f"%{q}%", f"%{q}%"]
        order = {"path": "path", "newest": "id DESC", "score": "json_extract(vlm_json,'$.quality_score') DESC, path",
                 "sharpness": "json_extract(local_json,'$.primary_head_sharp') DESC",
                 "eye_sharpness": "json_extract(local_json,'$.primary_eye_sharp') DESC",
                 "lr": "json_extract(lr_json,'$.rating') DESC, path"}.get(sort, "path")
        w = " AND ".join(where)
        total = db.count(w, params)
        rows = db.rows(w, params, order=order, limit=limit, offset=offset)
        return {"total": total, "offset": offset, "items": [summary(r) for r in rows]}

    @app.get("/api/images/{img_id}")
    def get_image(img_id: int):
        r = db.row(img_id)
        if not r:
            raise HTTPException(404)
        return {**summary(r), "local": jcol(r, "local_json"),
                "vlm": jcol(r, "vlm_json"),
                "override": jcol(r, "override_json"),
                "usage": jcol(r, "vlm_usage"),
                "final": sorter.final_record(r, cfg.get("focus_source", "vlm"))}

    @app.get("/api/images/{img_id}/vlm-request")
    def vlm_request(img_id: int, backend: Optional[str] = None, model: Optional[str] = None):
        """The request the vision model gets for this image, as the backend builds it, with image bytes
        replaced by their size. Built from the current cache and config, so it matches what was sent as long
        as neither changed since."""
        from .. import backends
        r = db.row(img_id)
        if not r or not r["local_json"]:
            raise HTTPException(404, "not analyzed")
        fp, cp = cache / f"{img_id}.jpg", cache / f"{img_id}_crop.jpg"
        if not fp.exists():
            raise HTTPException(404, "frame not in cache")
        bname = backend or cfg.get("backend", "ollama")
        try:
            be = backends.get(bname, cfg.get("base_url"))
        except SystemExit as e:
            raise HTTPException(400, str(e))
        mname = model or cfg.get("model") or be.default_model
        item = backends.load_item(r, cache)
        images = [{"label": "frame", "url": f"/media/frame/{img_id}", "bytes": fp.stat().st_size}]
        if cp.exists():
            images.append({"label": "crop", "url": f"/media/crop/{img_id}", "bytes": cp.stat().st_size})

        def redact(v):
            if isinstance(v, dict):
                return {k: redact(x) for k, x in v.items()}
            if isinstance(v, (list, tuple)):
                return [redact(x) for x in v]
            if isinstance(v, (bytes, bytearray)):
                return f"<{len(v) / 1024:.0f} KB image>"
            if isinstance(v, str) and len(v) > 4000 and " " not in v:
                return f"<base64 image, {len(v) * 3 / 4 / 1024:.0f} KB>"
            return v

        build = getattr(be, "build_request", None) or getattr(be, "build_params", None)
        request, build_error = None, None
        if build:
            try:
                request = json.loads(json.dumps(redact(build(item, mname, cfg)), default=str))
            except Exception as e:  # e.g. the cloud SDK isn't installed here
                build_error = f"{type(e).__name__}: {e}"
        return {"backend": bname, "model": mname, "system": schema.SYSTEM_PROMPT, "context": item.context,
                "images": images, "request": request, "build_error": build_error}

    @app.patch("/api/images/{img_id}")
    def override(img_id: int, o: OverrideIn):
        r = db.row(img_id)
        if not r:
            raise HTTPException(404)
        cur = jcol(r, "override_json", {})
        if o.clear:
            cur = {}
        else:
            for k, v in o.model_dump(exclude={"clear"}, exclude_none=True).items():
                cur[k] = v
            # A rating or a focus tier is your verdict on the photo: the two stay in step, and the photo counts as
            # reviewed. Only another rating or a reset changes it; jobs and rescans never write override_json.
            if o.rating is not None or o.focus_tier is not None:
                cur["rating"] = o.rating if o.rating is not None else o.focus_tier
                cur["focus_tier"] = min(cur["rating"], 3)
                cur["reviewed"], cur["reviewed_at"] = True, time.time()
        db.set_override(img_id, cur or None)
        return get_image(img_id)

    # ---- media ----------------------------------------------------------------------
    def media(img_id: int, suffix: str):
        p = cache / f"{img_id}{suffix}.jpg"
        if not p.exists():
            raise HTTPException(404)
        return FileResponse(p, media_type="image/jpeg", headers={"Cache-Control": "private, max-age=86400"})

    @app.get("/media/thumb/{img_id}")
    def thumb(img_id: int):
        return media(img_id, "_thumb")

    @app.get("/media/frame/{img_id}")
    def frame(img_id: int):
        return media(img_id, "")

    @app.get("/media/crop/{img_id}")
    def crop(img_id: int):
        return media(img_id, "_crop")

    @app.get("/media/full/{img_id}")
    def full(img_id: int):
        """The original at native resolution for the zoomable viewer; rendered once, then served from the cache."""
        p = cache / f"{img_id}_full.jpg"
        if not p.exists():
            r = db.row(img_id)
            if not r or not Path(r["path"]).exists():
                raise HTTPException(404)
            p.parent.mkdir(parents=True, exist_ok=True)
            tmp = p.with_suffix(f".{os.getpid()}.{time.monotonic_ns()}.tmp")
            tmp.write_bytes(I.to_jpeg(I.load_rgb(Path(r["path"])), 92))
            tmp.replace(p)
        return media(img_id, "_full")

    debug_cache: dict = {}   # (id, local_json hash) -> focus_debug result; a few recent images only

    @app.get("/api/images/{img_id}/focus-debug")
    def focus_debug(img_id: int):
        """Eye crops, Laplacian maps, FFT spectra and a sharpness heatmap, recomputed from the original file."""
        from ..debugviz import focus_debug as _fd
        r = db.row(img_id)
        if not r or not r["local_json"]:
            raise HTTPException(404, "not analyzed")
        key = (img_id, hash(r["local_json"]))
        if key not in debug_cache:
            path = Path(r["path"])
            if not path.exists():
                raise HTTPException(404, "original file not found")
            if len(debug_cache) >= 8:
                debug_cache.pop(next(iter(debug_cache)))
            debug_cache[key] = _fd(path, json.loads(r["local_json"]))
        return debug_cache[key]

    # ---- config / calibration --------------------------------------------------------
    @app.get("/api/config")
    def get_config():
        return {k: v for k, v in cfg.items() if not k.startswith("_")}

    @app.put("/api/config")
    def put_config(c: ConfigIn):
        for k, v in c.values.items():
            if isinstance(v, dict) and isinstance(cfg.get(k), dict):
                cfg[k].update(v)
            else:
                cfg[k] = v
        (workdir / "config.json").write_text(json.dumps(cfg, indent=2))
        runner.reload_config.set()
        return get_config()

    @app.post("/api/rescore")
    def rescore():
        from ..local import rescore as _rescore
        return _rescore(db, cfg)

    @app.get("/api/calibration")
    def calibration(n: int = 48, metric: str = "eye"):
        import numpy as np
        if metric not in truth.METRICS:
            raise HTTPException(400, f"metric must be one of {sorted(truth.METRICS)}")
        path = truth.METRICS[metric][0]
        vals = [tuple(r) for r in db.conn.execute(
            f"SELECT json_extract(local_json, '{path}') v, id, json_extract(local_json, '$.local_tier') FROM images "
            "WHERE local_json IS NOT NULL AND v IS NOT NULL ORDER BY v, id")]
        base = {"metric": metric, "thresholds": cfg["focus"], "keys": truth.METRICS[metric][1:]}
        if not vals:
            return {**base, "percentiles": {}, "samples": []}
        arr = np.array([v[0] for v in vals])
        idx = np.linspace(0, len(vals) - 1, min(n, len(vals))).astype(int)
        return {**base, "count": len(vals), "percentiles": {f"p{q}": round(float(np.percentile(arr, q)), 4) for q in (5, 10, 25, 50, 75, 90, 95)},
                "samples": [{"id": vals[i][1], "sharp": vals[i][0], "tier": vals[i][2]} for i in idx]}

    # ---- ground truth -----------------------------------------------------------------
    def truth_summary():
        """Confusion matrices and suggested thresholds from the images that carry a verdict (usually a few
        hundred), read in one pass instead of one scan of the whole table per matrix and metric."""
        c = db.conn
        paths = {m: v[0] for m, v in truth.METRICS.items()}
        rows = c.execute(
            "SELECT json_extract(truth_json,'$.focus_tier') truth, json_extract(local_json,'$.local_tier') local, "
            "json_extract(vlm_json,'$.focus_tier') vlm, "
            + ", ".join(f"json_extract(local_json,'{p}') {m}" for m, p in paths.items())
            + " FROM images WHERE truth_json IS NOT NULL").fetchall()
        with_tier = [r for r in rows if r["truth"] is not None]

        def matrix(col):
            n: dict[tuple, int] = {}
            for r in with_tier:
                if r[col] is not None:
                    n[(r["truth"], r[col])] = n.get((r["truth"], r[col]), 0) + 1
            return [{"truth": t, "pred": p, "n": k} for (t, p), k in sorted(n.items())]

        def acc(m):
            tot = sum(r["n"] for r in m); ok = sum(r["n"] for r in m if r["truth"] == r["pred"])
            return round(ok / tot, 3) if tot else None
        local_m, vlm_m = matrix("local"), matrix("vlm")
        suggested = {}
        for m, (_, *keys) in truth.METRICS.items():
            pairs = [(r[m], int(r["truth"])) for r in with_tier if r[m] is not None]
            sug = truth.suggest_thresholds(pairs)
            if sug:   # suggest_thresholds names the head keys; keys[i] is this metric's key for tier 3 - i
                suggested[m] = {"n": len(pairs), **{keys[3 - int(k[4])]: v for k, v in sug.items()}}
        return {"images_with_truth": len(rows), "with_tier": len(with_tier), "local": {"matrix": local_m, "accuracy": acc(local_m)},
                "vlm": {"matrix": vlm_m, "accuracy": acc(vlm_m)}, "suggested": suggested,
                "mapping": cfg.get("truth")}

    @app.get("/api/truth")
    def get_truth():
        return truth_summary()

    @app.post("/api/truth/upload")
    async def truth_upload(files: list[UploadFile] = File(...), folder: str = ""):
        pairs = [(f.filename or "x", await f.read()) for f in files]
        verdicts = truth.parse_files(pairs)
        res = truth.apply(db, verdicts, cfg, str(safe_path(folder)) if folder else None)
        return {**res, "summary": truth_summary()}

    @app.post("/api/truth/import")
    def truth_import(t: TruthImportIn):
        d = Path(t.dir)
        if not d.is_absolute():
            d = photos_root / t.dir if (photos_root / t.dir).exists() else workdir / t.dir
        if not d.is_dir():
            raise HTTPException(404, f"no such directory: {d}")
        verdicts = truth.load_dir(d)
        res = truth.apply(db, verdicts, cfg, str(safe_path(t.folder)) if t.folder else None)
        return {**res, "summary": truth_summary()}

    @app.delete("/api/truth")
    def truth_clear():
        return {"cleared": db.clear_truth()}

    # ---- export -----------------------------------------------------------------------
    @app.post("/api/export")
    def export(e: ExportIn):
        out = workdir / "exports" / Path(e.name).name
        where, params = ["local_json IS NOT NULL"], []
        if e.folder:
            base = str(safe_path(e.folder))
            w, p = under_folder(base)
            where.append(w); params += p
        rows = db.rows(" AND ".join(where), params)
        recs = [sorter.final_record(r, e.focus_source or cfg.get("focus_source", "vlm")) for r in rows]
        sorter.export(recs, out)
        counts = sorter.build_tree(recs, out, e.link) if e.tree else {}
        w = s = 0
        if e.xmp:
            w, s = sorter.write_xmp(recs, out / "xmp", True)
        return {"out": str(out), "images": len(recs), "tree": counts, "xmp_written": w}

    @app.get("/api/exports")
    def exports():
        d = workdir / "exports"
        if not d.exists():
            return []
        return [{"name": p.name, "path": str(p), "mtime": p.stat().st_mtime} for p in sorted(d.iterdir()) if p.is_dir()]

    # Local dev convenience: serve the built UI if present (nginx does this in production).
    dist = Path(__file__).resolve().parents[2] / "web" / "dist"
    if dist.exists():
        app.mount("/assets", StaticFiles(directory=dist / "assets"), name="assets")

        @app.get("/{spa_path:path}", include_in_schema=False)
        def spa(spa_path: str):
            f = dist / spa_path
            if spa_path and f.is_file():
                return FileResponse(f)
            return FileResponse(dist / "index.html")  # SPA fallback (nginx try_files does this in prod)
    return app
