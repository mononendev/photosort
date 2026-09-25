from __future__ import annotations
import argparse
import json
import os
import random
import sys
import time
from pathlib import Path

from tqdm import tqdm

from . import config, images as I, schema
from .db import DB


def _ctx(args):
    workdir = Path(args.workdir)
    workdir.mkdir(parents=True, exist_ok=True)
    cfg = config.load(workdir)
    return workdir, cfg, DB(workdir / "photosort.db")


def cmd_scan(args):
    workdir, cfg, db = _ctx(args)
    paths = []
    for d in args.dirs:
        d = Path(d)
        if d.is_file() and I.is_image(d):
            paths.append(d)
        else:
            paths += [p for p in d.rglob("*") if p.is_file() and I.is_image(p)]
    if args.skip_raw_dupes:
        # when a JPEG and RAW share a stem, keep only the JPEG (faster to decode)
        jpg_stems = {p.with_suffix("").as_posix() for p in paths if p.suffix.lower() not in I.RAW_EXT}
        paths = [p for p in paths if p.suffix.lower() not in I.RAW_EXT or p.with_suffix("").as_posix() not in jpg_stems]
    n = db.add_paths(paths)
    from . import sidecar
    m = sidecar.ingest(db, db.rows("lr_json IS NULL"))
    print(f"found {len(paths)} images, {n} new ({m} with Lightroom sidecars); total tracked {db.count()}")


def cmd_local(args):
    from . import local
    workdir, cfg, db = _ctx(args)
    where = "local_json IS NULL" + ("" if args.retry_errors else " AND error IS NULL")
    rows = db.rows(where)
    if args.limit:
        rows = rows[: args.limit]
    if not rows:
        print("nothing to do")
        return
    if args.workers:
        cfg["workers"] = args.workers
    print(f"local stage: {len(rows)} images, {cfg['workers']} workers")
    t = time.time()
    with tqdm(total=len(rows), unit="img") as bar:
        ok, err = local.run_local(db, cfg, workdir / "cache", [(r["id"], r["path"]) for r in rows], args.device, bar)
    dt = time.time() - t
    print(f"done: {ok} ok, {err} errors, {dt:.0f}s ({len(rows)/dt:.1f} img/s)")
    _local_summary(db)


def _local_summary(db):
    rows = db.rows("local_json IS NOT NULL")
    tiers = {0: 0, 1: 0, 2: 0}
    for r in rows:
        tiers[json.loads(r["local_json"])["local_tier"]] += 1
    print("local focus tiers:", {f"tier{k}": v for k, v in tiers.items()})


def cmd_calibrate(args):
    """Print the sharpness distribution and build a contact sheet of head crops sorted by sharpness."""
    from PIL import Image, ImageDraw
    workdir, cfg, db = _ctx(args)
    rows = db.rows("local_json IS NOT NULL")
    vals = []
    for r in rows:
        d = json.loads(r["local_json"])
        if d["primary_head_sharp"] is not None:
            vals.append((d["primary_head_sharp"], r["id"], r["path"], d))
    if not vals:
        print("no local results yet")
        return
    vals.sort()
    import numpy as np
    arr = np.array([v[0] for v in vals])
    print(f"{len(vals)} images with a primary subject. Head sharpness percentiles:")
    for q in (5, 10, 25, 50, 75, 90, 95):
        print(f"  p{q:<3d} {np.percentile(arr, q):.4f}")
    print(f"current thresholds: tier2 >= {cfg['focus']['tier2_min']}, tier1 >= {cfg['focus']['tier1_min']}")
    # contact sheet: N tiles evenly spaced across the sorted range
    n = min(args.tiles, len(vals))
    idx = np.linspace(0, len(vals) - 1, n).astype(int)
    tile, cols = 256, 8
    rows_n = -(-n // cols)
    sheet = Image.new("RGB", (cols * tile, rows_n * (tile + 28)), "black")
    draw = ImageDraw.Draw(sheet)
    for k, i in enumerate(idx):
        s, img_id, path, d = vals[i]
        cp = workdir / "cache" / f"{img_id}_crop.jpg"
        try:
            im = Image.open(cp) if cp.exists() else Image.open(workdir / "cache" / f"{img_id}.jpg")
            head = d["people"][0]["head"]; cb = d.get("crop_box")
            if cp.exists() and cb:  # map head box into crop coords
                sx = im.size[0] / (cb[2] - cb[0])
                hb = [(head[0] - cb[0]) * sx, (head[1] - cb[1]) * sx, (head[2] - cb[0]) * sx, (head[3] - cb[1]) * sx]
                im = im.crop((max(0, int(hb[0] - 20)), max(0, int(hb[1] - 20)), min(im.size[0], int(hb[2] + 20)), min(im.size[1], int(hb[3] + 20))))
            im.thumbnail((tile, tile))
        except Exception:
            im = Image.new("RGB", (tile, tile), "gray")
        x, y = (k % cols) * tile, (k // cols) * (tile + 28)
        sheet.paste(im, (x, y))
        draw.text((x + 4, y + tile + 4), f"{s:.4f} t{d['local_tier']} {Path(path).name[:22]}", fill="white")
    out = workdir / "calibration_sheet.jpg"
    sheet.save(out, quality=85)
    print(f"contact sheet (sharpness ascending, left-to-right, top-to-bottom): {out}")
    print("Pick the sharpness values where 'soft' becomes 'acceptable' and 'acceptable' becomes 'crisp',")
    print(f"then set focus.tier1_min / focus.tier2_min in {workdir/'config.json'} and re-run `local --rescore`.")


def cmd_rescore(args):
    """Re-derive local tiers from stored metrics with the current thresholds (no re-detection)."""
    from .local import local_tier
    workdir, cfg, db = _ctx(args)
    n = 0
    for r in db.rows("local_json IS NOT NULL"):
        d = json.loads(r["local_json"])
        people = d.get("people") or []
        tier, reason = local_tier(people[0] if people else None, people[1:], cfg["focus"])
        if tier != d["local_tier"]:
            n += 1
        d["local_tier"], d["local_reason"] = tier, reason
        db.set_local(r["id"], d)
    print(f"rescored; {n} images changed tier")
    _local_summary(db)


def _estimate(backend, model, db, cfg, rows=None):
    rows = rows if rows is not None else db.rows("local_json IS NOT NULL AND vlm_json IS NULL AND batch_id IS NULL")
    with_crop = sum(1 for r in rows if json.loads(r["local_json"])["n_people"] > 0)
    return backend.estimate(model, with_crop, len(rows) - with_crop, cfg), len(rows)


def cmd_estimate(args):
    from . import backends
    workdir, cfg, db = _ctx(args)
    n = db.count("local_json IS NOT NULL AND vlm_json IS NULL AND batch_id IS NULL") or db.count()
    rows = db.rows("local_json IS NOT NULL")
    with_crop = sum(1 for r in rows if json.loads(r["local_json"])["n_people"] > 0) if rows else int(n * 0.9)
    without = (len(rows) - with_crop) if rows else n - with_crop
    print(f"{n} images pending ({with_crop} with a subject crop). Batch price is what you pay; interactive shown for reference.")
    print(f"{'model':26s} {'in tokens':>12s} {'out tokens':>11s} {'interactive':>12s} {'batch':>9s}")
    print(f"{'ollama (local, qwen3-vl)':26s} {'-':>12s} {'-':>11s} {'free':>12s} {'free':>9s}   (GPU time instead; see STATUS.md)")
    for m in backends.PRICES:
        b = backends.get("gemini" if m.startswith("gemini") else "anthropic")
        e = b.estimate(m, with_crop, without, cfg)
        print(f"{m:26s} {e['input_tokens']:>12,d} {e['output_tokens']:>11,d} {e['interactive_usd']:>11.2f}$ {e['batch_usd']:>8.2f}$")


def _items_for(rows, workdir):
    from .backends import Item
    items = []
    for r in rows:
        local = json.loads(r["local_json"])
        frame = (workdir / "cache" / f"{r['id']}.jpg").read_bytes()
        cp = workdir / "cache" / f"{r['id']}_crop.jpg"
        items.append(Item(str(r["id"]), frame, cp.read_bytes() if cp.exists() else None, schema.context_text(local)))
    return items


def cmd_submit(args):
    from . import backends
    workdir, cfg, db = _ctx(args)
    bname = args.backend or cfg["backend"]
    backend = backends.get(bname, args.base_url or cfg.get("base_url"))
    model = args.model or cfg.get("model") or backend.default_model
    rows = db.rows("local_json IS NOT NULL AND vlm_json IS NULL AND batch_id IS NULL" + ("" if args.retry_errors else " AND error IS NULL"))
    if args.skip_local_tier0:
        rows = [r for r in rows if json.loads(r["local_json"])["local_tier"] > 0]
    if args.sample:
        random.Random(args.seed).shuffle(rows)
        rows = rows[: args.sample]
    if not rows:
        print("nothing to submit (run `local` first, or everything is already submitted)")
        return
    est, _ = _estimate(backend, model, db, cfg, rows)
    print(f"backend={bname} model={model} images={len(rows)} est. input tokens={est['input_tokens']:,} "
          f"batch cost ≈ ${est['batch_usd']}" + ("" if est["priced"] else " (model not in price table)"))
    if args.dry_run:
        if bname == "gemini":
            p = backend.write_jsonl(_items_for(rows[:3], workdir), model, cfg, workdir / "dry-run.jsonl")
            print(f"dry run: wrote 3 requests to {p} ({p.stat().st_size//1024} KB); nothing uploaded")
        else:
            params = backend.build_params(_items_for(rows[:1], workdir)[0], model, cfg)
            params["messages"][0]["content"] = [c if c["type"] == "text" else {"type": "image", "bytes": len(c["source"]["data"])} for c in params["messages"][0]["content"]]
            print(json.dumps(params, indent=1)[:3000])
        return
    if getattr(backend, "sync", False):
        return _run_sync(backend, model, rows, cfg, workdir, db, args.concurrency)
    if not args.yes:
        ans = input("submit? [y/N] ").strip().lower()
        if ans != "y":
            return
    size = min(args.batch_size or cfg["batch_size"], backend.max_batch)
    for i in range(0, len(rows), size):
        chunk = rows[i:i + size]
        items = _items_for(chunk, workdir)
        bid = backend.submit(items, model, cfg, workdir)
        db.add_batch(bid, bname, model, len(chunk), time.time())
        db.set_batch([r["id"] for r in chunk], bid)
        print(f"submitted batch {bid} ({len(chunk)} images)")


def _run_sync(backend, model, rows, cfg, workdir, db, concurrency):
    """Synchronous backends (local model server): classify each image now, store as we go."""
    from concurrent.futures import ThreadPoolExecutor, as_completed
    print(f"running {len(rows)} images through {backend.name} at {backend.base_url} with {model}, concurrency {concurrency}")
    t0 = time.time(); ok = err = 0; secs = []
    with ThreadPoolExecutor(max_workers=concurrency) as ex, tqdm(total=len(rows), unit="img") as bar:
        futs = {ex.submit(backend.classify, it, model, cfg): it for it in _items_for(rows, workdir)}
        for f in as_completed(futs):
            res = f.result()
            img_id = int(res.key)
            if res.data:
                db.set_vlm(img_id, res.data, res.usage, None); ok += 1
            else:
                db.set_vlm(img_id, None, res.usage, res.error); err += 1
            if res.usage and res.usage.get("seconds"):
                secs.append(res.usage["seconds"])
            bar.update(1)
            if secs and len(secs) % 20 == 1:
                bar.set_postfix(avg_s=f"{sum(secs)/len(secs):.1f}", err=err)
    dt = time.time() - t0
    print(f"done: {ok} ok, {err} errors, {dt:.0f}s total, {dt/max(1,len(rows)):.1f}s/img wall, "
          f"{(sum(secs)/len(secs)) if secs else 0:.1f}s/img server latency")
    _usage_summary(db)


def cmd_poll(args):
    from . import backends
    workdir, cfg, db = _ctx(args)
    while True:
        pending = db.batches(only_unfetched=True)
        if not pending:
            print("no pending batches")
            break
        remaining = 0
        for b in pending:
            backend = backends.get(b["backend"])
            st = backend.status(b["id"])
            if st == "ended":
                results = backend.fetch(b["id"])
                ok = 0
                for res in results:
                    try:
                        img_id = int(res.key)
                    except (TypeError, ValueError):
                        continue
                    if res.data:
                        db.set_vlm(img_id, res.data, res.usage, None)
                        ok += 1
                    else:
                        db.set_vlm(img_id, None, None, res.error)
                db.set_batch_state(b["id"], "ended", fetched=True)
                print(f"batch {b['id']}: {ok}/{len(results)} parsed ok")
                # anything submitted but missing from results can be resubmitted
                db.conn.execute("UPDATE images SET batch_id=NULL WHERE batch_id=? AND vlm_json IS NULL AND error IS NULL", (b["id"],))
                db.conn.commit()
            elif st.startswith("failed"):
                db.set_batch_state(b["id"], st, fetched=True)
                db.clear_batch(b["id"])
                print(f"batch {b['id']}: {st}; images released for resubmission")
            else:
                remaining += 1
                db.set_batch_state(b["id"], st)
        if not remaining or not args.wait:
            if remaining:
                print(f"{remaining} batch(es) still running; re-run `poll` or use --wait")
            break
        time.sleep(args.interval)
    _usage_summary(db)


def _usage_summary(db):
    rows = db.rows("vlm_usage IS NOT NULL")
    if not rows:
        return
    tot_in = sum((json.loads(r["vlm_usage"]).get("in") or 0) for r in rows)
    tot_out = sum((json.loads(r["vlm_usage"]).get("out") or 0) for r in rows)
    print(f"results so far: {len(rows)} images, {tot_in:,} input tokens, {tot_out:,} output tokens "
          f"(avg {tot_in//max(1,len(rows)):,} in / {tot_out//max(1,len(rows)):,} out per image)")


def cmd_sort(args):
    from . import sort
    workdir, cfg, db = _ctx(args)
    source = args.focus_source or cfg["focus_source"]
    rows = db.rows("local_json IS NOT NULL")
    recs = [sort.final_record(r, source) for r in rows]
    out = Path(args.out)
    sort.export(recs, out)
    counts = sort.build_tree(recs, out, args.link)
    print(f"sorted {sum(v for k, v in counts.items() if k != 'review')} images into {out} using {args.link}: {counts}")
    if args.xmp != "none":
        xd = None if args.xmp == "sidecar" else out / "xmp"
        w, s = sort.write_xmp(recs, xd, args.xmp_overwrite)
        print(f"XMP: wrote {w}, skipped {s} existing" + ("" if w or not s else " (use --xmp-overwrite)"))
    print(f"exports: {out/'results.csv'}, {out/'results.jsonl'}")


def cmd_web(args):
    import uvicorn
    from .web.app import create_app
    app = create_app(Path(args.workdir), Path(args.photos).resolve(), args.device)
    uvicorn.run(app, host=args.host, port=args.port, log_level="info")


def cmd_status(args):
    workdir, cfg, db = _ctx(args)
    print(f"workdir: {workdir}")
    print(f"images: {db.count()} tracked, {db.count('local_json IS NOT NULL')} local done, "
          f"{db.count('batch_id IS NOT NULL AND vlm_json IS NULL')} in flight, {db.count('vlm_json IS NOT NULL')} tagged, "
          f"{db.count('error IS NOT NULL')} errors")
    for b in db.batches():
        print(f"  batch {b['id']} {b['backend']}/{b['model']} n={b['n']} state={b['state']} fetched={b['fetched']}")
    if db.count("local_json IS NOT NULL"):
        _local_summary(db)
    _usage_summary(db)
    errs = db.rows("error IS NOT NULL")[:5]
    for e in errs:
        print(f"  error: {Path(e['path']).name}: {e['error'][:120]}")


def main(argv=None):
    ap = argparse.ArgumentParser(prog="photosort", description="Cull and tag event photos: local focus scoring + cloud vision tagging.")
    ap.add_argument("--workdir", default=os.environ.get("PHOTOSORT_WORKDIR", "photosort_work"), help="state, cache and batch files (default ./photosort_work or $PHOTOSORT_WORKDIR)")
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("scan", help="register image files"); p.add_argument("dirs", nargs="+")
    p.add_argument("--skip-raw-dupes", action="store_true", help="if a JPEG and RAW share a name, only use the JPEG"); p.set_defaults(fn=cmd_scan)

    p = sub.add_parser("local", help="stage 1: detect people, score sharpness, build frames/crops")
    p.add_argument("--workers", type=int); p.add_argument("--device", help="cuda | mps | cpu (auto)")
    p.add_argument("--limit", type=int); p.add_argument("--retry-errors", action="store_true"); p.set_defaults(fn=cmd_local)

    p = sub.add_parser("calibrate", help="show sharpness distribution + contact sheet to pick focus thresholds")
    p.add_argument("--tiles", type=int, default=64); p.set_defaults(fn=cmd_calibrate)
    p = sub.add_parser("rescore", help="re-derive local tiers with current thresholds"); p.set_defaults(fn=cmd_rescore)

    p = sub.add_parser("estimate", help="cost table for pending images across models"); p.set_defaults(fn=cmd_estimate)

    p = sub.add_parser("submit", help="stage 2: submit batch job(s) to the vision model")
    p.add_argument("--backend", choices=["gemini", "anthropic", "ollama"]); p.add_argument("--model")
    p.add_argument("--base-url", help="ollama server, e.g. http://k8s-5:11434 (or OLLAMA_HOST)")
    p.add_argument("--concurrency", type=int, default=2, help="parallel requests for sync backends (match OLLAMA_NUM_PARALLEL)")
    p.add_argument("--skip-local-tier0", action="store_true", help="don't send images the local stage scored as nothing-in-focus")
    p.add_argument("--retry-errors", action="store_true")
    p.add_argument("--sample", type=int, help="submit only N random images (calibration run)"); p.add_argument("--seed", type=int, default=0)
    p.add_argument("--batch-size", type=int); p.add_argument("--dry-run", action="store_true"); p.add_argument("-y", "--yes", action="store_true")
    p.set_defaults(fn=cmd_submit)

    p = sub.add_parser("poll", help="check batches, fetch finished results")
    p.add_argument("--wait", action="store_true"); p.add_argument("--interval", type=int, default=120); p.set_defaults(fn=cmd_poll)

    p = sub.add_parser("sort", help="stage 3: link tree + CSV/JSONL + XMP")
    p.add_argument("out"); p.add_argument("--link", choices=["symlink", "hardlink", "copy", "move"], default="symlink")
    p.add_argument("--focus-source", choices=["vlm", "local", "strict"])
    p.add_argument("--xmp", choices=["sidecar", "outdir", "none"], default="sidecar", help="sidecar = next to originals")
    p.add_argument("--xmp-overwrite", action="store_true"); p.set_defaults(fn=cmd_sort)

    p = sub.add_parser("status", help="pipeline status"); p.set_defaults(fn=cmd_status)

    p = sub.add_parser("web", help="run the API server + job worker")
    p.add_argument("--photos", default=os.environ.get("PHOTOSORT_PHOTOS", "."), help="photos root (read-only is fine)")
    p.add_argument("--host", default="0.0.0.0"); p.add_argument("--port", type=int, default=int(os.environ.get("PORT", "8080")))
    p.add_argument("--device", help="cuda | mps | cpu (auto)"); p.set_defaults(fn=cmd_web)

    args = ap.parse_args(argv)
    args.fn(args)


if __name__ == "__main__":
    main()
