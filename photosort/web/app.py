"""FastAPI backend for the photosort UI. Static assets are served by the UI container (nginx)."""
import json
import os
import time
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, File, HTTPException, Query, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from .. import __version__, config, images as I, schema, sort as sorter, truth
from ..db import DB
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
    focus_tier: Optional[int] = None
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

    @app.on_event("startup")
    def _start():
        runner.start()

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
        local = json.loads(row["local_json"]) if row["local_json"] else None
        lr = json.loads(row["lr_json"]) if row["lr_json"] else {}
        tr = json.loads(row["truth_json"]) if row["truth_json"] else {}
        return {
            "id": row["id"], "path": row["path"], "rel": rel(row["path"]), "name": Path(row["path"]).name,
            "folder": rel(row["folder"] or str(Path(row["path"]).parent)),
            "status": "tagged" if row["vlm_json"] else ("analyzed" if row["local_json"] else ("error" if row["error"] else "pending")),
            "has_crop": bool(local and local.get("n_people")),
            "focus_tier": rec["focus_tier"], "focus_tier_local": rec["focus_tier_local"], "focus_tier_vlm": rec["focus_tier_vlm"],
            "review": rec["review"], "subject": rec["subject"], "composition": rec["composition"],
            "quality_score": rec["quality_score"], "keeper": rec["keeper"], "overridden": rec["overridden"],
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
                "device": dev, "backend": cfg.get("backend"), "ollama": cfg.get("base_url"), "current_job": runner.current}

    @app.get("/api/stats")
    def stats():
        c = db.conn
        tiers = {0: 0, 1: 0, 2: 0}
        for r in c.execute("SELECT local_json, vlm_json, override_json FROM images WHERE local_json IS NOT NULL"):
            rec = sorter.final_record(r, cfg.get("focus_source", "vlm")) if False else None  # cheap path below
            src = r["override_json"] or r["vlm_json"] or r["local_json"]
            d = json.loads(src)
            t = d.get("focus_tier", d.get("local_tier"))
            if t is not None:
                tiers[int(t)] += 1
        return {"tracked": db.count(), "analyzed": db.count("local_json IS NOT NULL"), "tagged": db.count("vlm_json IS NOT NULL"),
                "errors": db.count("error IS NOT NULL"), "review": db.count("local_json IS NOT NULL AND vlm_json IS NOT NULL AND json_extract(local_json,'$.local_tier') != json_extract(vlm_json,'$.focus_tier')"),
                "tiers": {f"tier{k}": v for k, v in tiers.items()},
                "keepers": db.count("json_extract(vlm_json,'$.keeper') = 1"),
                "lr_rated": db.count("json_extract(lr_json,'$.rating') > 0"),
                "lr_by_tier": [dict(r) for r in c.execute(
                    "SELECT COALESCE(json_extract(override_json,'$.focus_tier'), json_extract(vlm_json,'$.focus_tier'), json_extract(local_json,'$.local_tier')) tier, "
                    "json_extract(lr_json,'$.rating') rating, COUNT(*) n FROM images WHERE local_json IS NOT NULL AND json_extract(lr_json,'$.rating') IS NOT NULL "
                    "GROUP BY tier, rating ORDER BY tier, rating")]}

    # ---- browse -----------------------------------------------------------------
    @app.get("/api/tree")
    def tree(path: str = ""):
        base = safe_path(path)
        if not base.is_dir():
            raise HTTPException(404, "not a directory")
        fstats = db.folder_stats(str(base))
        dirs, files = [], []
        try:
            entries = sorted(os.scandir(base), key=lambda e: e.name.lower())
        except PermissionError:
            raise HTTPException(403, "permission denied")
        for e in entries:
            if e.name.startswith("."):
                continue
            if e.is_dir(follow_symlinks=False):
                sub = str(Path(e.path))
                agg = {"tracked": 0, "local_done": 0, "vlm_done": 0, "errors": 0}
                for folder, st in fstats.items():
                    if folder == sub or folder.startswith(sub + "/"):
                        agg["tracked"] += st["n"]; agg["local_done"] += st["local_done"] or 0
                        agg["vlm_done"] += st["vlm_done"] or 0; agg["errors"] += st["errors"] or 0
                try:
                    n_direct = sum(1 for x in os.scandir(e.path) if x.is_file() and I.is_image(Path(x.name)))
                except PermissionError:
                    n_direct = 0
                dirs.append({"name": e.name, "path": rel(e.path), "images_direct": n_direct, **agg})
            elif e.is_file() and I.is_image(Path(e.name)):
                row = db.conn.execute("SELECT * FROM images WHERE path=?", (e.path,)).fetchone()
                files.append(summary(row) if row else {"id": None, "name": e.name, "rel": rel(e.path), "path": e.path, "status": "untracked"})
        return {"path": rel(str(base)) if base != photos_root else "", "dirs": dirs, "files": files}

    # ---- jobs -------------------------------------------------------------------
    def job_out(j) -> dict:
        d = dict(j)
        d["paths"] = json.loads(d.pop("paths_json") or "[]")
        d["options"] = json.loads(d.pop("options_json") or "{}")
        if d["started"] and d["done"] and d["state"] == "running":
            el = time.time() - d["started"]
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

    @app.post("/api/jobs/{job_id}/cancel")
    def cancel_job(job_id: int):
        j = db.job(job_id)
        if not j:
            raise HTTPException(404)
        if j["state"] == "queued":
            db.update_job(job_id, state="cancelled", finished=time.time())
        elif j["state"] == "running":
            db.update_job(job_id, state="cancelling")
        return job_out(db.job(job_id))

    # ---- images -------------------------------------------------------------------
    @app.get("/api/images")
    def list_images(folder: str = "", recursive: bool = True, tier: Optional[int] = None, keeper: Optional[bool] = None,
                    subject: Optional[str] = None, status: Optional[str] = None, review: Optional[bool] = None,
                    lr_rating: Optional[int] = None, lr_label: Optional[str] = None,
                    truth_tier: Optional[int] = None, truth_mismatch: Optional[bool] = None,
                    q: Optional[str] = None, sort: str = "path", offset: int = 0, limit: int = Query(60, le=500)):
        where, params = ["1"], []
        if folder:
            base = str(safe_path(folder))
            if recursive:
                where.append("(folder = ? OR folder LIKE ?)"); params += [base, base + "/%"]
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
            where.append("COALESCE(json_extract(override_json,'$.focus_tier'), json_extract(vlm_json,'$.focus_tier'), json_extract(local_json,'$.local_tier')) = ?")
            params.append(tier)
        if keeper is not None:
            where.append("COALESCE(json_extract(override_json,'$.keeper'), json_extract(vlm_json,'$.keeper')) = ?"); params.append(int(keeper))
        if subject:
            where.append("json_extract(vlm_json,'$.primary_subject') = ?"); params.append(subject)
        if review:
            where.append("local_json IS NOT NULL AND vlm_json IS NOT NULL AND json_extract(local_json,'$.local_tier') != json_extract(vlm_json,'$.focus_tier')")
        if lr_rating is not None:
            where.append("json_extract(lr_json,'$.rating') = ?"); params.append(lr_rating)
        if lr_label:
            where.append("json_extract(lr_json,'$.label') = ?"); params.append(lr_label)
        if truth_tier is not None:
            where.append("json_extract(truth_json,'$.focus_tier') = ?"); params.append(truth_tier)
        if truth_mismatch:
            where.append("json_extract(truth_json,'$.focus_tier') IS NOT NULL AND local_json IS NOT NULL AND "
                         "json_extract(truth_json,'$.focus_tier') != COALESCE(json_extract(override_json,'$.focus_tier'), json_extract(vlm_json,'$.focus_tier'), json_extract(local_json,'$.local_tier'))")
        if q:
            where.append("(path LIKE ? OR vlm_json LIKE ?)"); params += [f"%{q}%", f"%{q}%"]
        order = {"path": "path", "newest": "id DESC", "score": "json_extract(vlm_json,'$.quality_score') DESC, path",
                 "sharpness": "json_extract(local_json,'$.primary_head_sharp') DESC",
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
        return {**summary(r), "local": json.loads(r["local_json"]) if r["local_json"] else None,
                "vlm": json.loads(r["vlm_json"]) if r["vlm_json"] else None,
                "override": json.loads(r["override_json"]) if r["override_json"] else None,
                "usage": json.loads(r["vlm_usage"]) if r["vlm_usage"] else None,
                "final": sorter.final_record(r, cfg.get("focus_source", "vlm"))}

    @app.patch("/api/images/{img_id}")
    def override(img_id: int, o: OverrideIn):
        r = db.row(img_id)
        if not r:
            raise HTTPException(404)
        cur = json.loads(r["override_json"]) if r["override_json"] else {}
        if o.clear:
            cur = {}
        else:
            for k, v in o.model_dump(exclude={"clear"}, exclude_none=True).items():
                cur[k] = v
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
    def calibration(n: int = 48):
        import numpy as np
        vals = []
        for r in db.rows("local_json IS NOT NULL"):
            d = json.loads(r["local_json"])
            if d.get("primary_head_sharp") is not None:
                vals.append((d["primary_head_sharp"], r["id"], d["local_tier"]))
        if not vals:
            return {"percentiles": {}, "samples": []}
        vals.sort()
        arr = np.array([v[0] for v in vals])
        idx = np.linspace(0, len(vals) - 1, min(n, len(vals))).astype(int)
        return {"count": len(vals), "percentiles": {f"p{q}": round(float(np.percentile(arr, q)), 4) for q in (5, 10, 25, 50, 75, 90, 95)},
                "thresholds": cfg["focus"], "samples": [{"id": vals[i][1], "sharp": vals[i][0], "tier": vals[i][2]} for i in idx]}

    # ---- ground truth -----------------------------------------------------------------
    def truth_summary():
        c = db.conn
        n = db.count("truth_json IS NOT NULL")
        with_tier = db.count("json_extract(truth_json,'$.focus_tier') IS NOT NULL")
        def matrix(col):
            return [dict(r) for r in c.execute(
                f"SELECT json_extract(truth_json,'$.focus_tier') truth, {col} pred, COUNT(*) n FROM images "
                f"WHERE json_extract(truth_json,'$.focus_tier') IS NOT NULL AND {col} IS NOT NULL GROUP BY truth, pred")]
        local_m = matrix("json_extract(local_json,'$.local_tier')")
        vlm_m = matrix("json_extract(vlm_json,'$.focus_tier')")
        def acc(m):
            tot = sum(r["n"] for r in m); ok = sum(r["n"] for r in m if r["truth"] == r["pred"])
            return round(ok / tot, 3) if tot else None
        pairs = [(r[0], int(r[1])) for r in c.execute(
            "SELECT json_extract(local_json,'$.primary_head_sharp'), json_extract(truth_json,'$.focus_tier') FROM images "
            "WHERE json_extract(truth_json,'$.focus_tier') IS NOT NULL AND json_extract(local_json,'$.primary_head_sharp') IS NOT NULL")]
        return {"images_with_truth": n, "with_tier": with_tier, "local": {"matrix": local_m, "accuracy": acc(local_m)},
                "vlm": {"matrix": vlm_m, "accuracy": acc(vlm_m)}, "suggested": truth.suggest_thresholds(pairs),
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
            where.append("(folder = ? OR folder LIKE ?)"); params += [base, base + "/%"]
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
