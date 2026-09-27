"""Detection parity and speed: every installed pose model against a reference (the original ultralytics yolo11n-pose.pt
by default) on the same images, as the local stage feeds them (long edge detect_long_edge, LANCZOS).

  uv run --extra ultralytics python scripts/parity.py IMAGES... [--ref yolo11n-pose.pt] [--models a,b] [--json out.json]

Per model: people found vs the reference (matched at IoU >= 0.5), mean box IoU and keypoint distance (in % of the box
diagonal) over the matches, confidence change, and seconds per image on this machine. Tier-level effects need the
rules, which live in Go: run `photosort local --detector NAME` into separate workdirs and compare them with
scripts/apidiff/compare_local.py.
"""
from __future__ import annotations
import argparse
import json
import statistics
import sys
import time
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from photosort_analyzer import detectors, images as I  # noqa: E402
from photosort_analyzer.metrics import dedup_detections  # noqa: E402


def iou(a, b):
    iw = max(0.0, min(a[2], b[2]) - max(a[0], b[0]))
    ih = max(0.0, min(a[3], b[3]) - max(a[1], b[1]))
    inter = iw * ih
    u = (a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter
    return inter / u if u > 0 else 0.0


def run(det, ims):
    out, secs = [], []
    det(ims[0])  # warm up
    for im in ims:
        t = time.perf_counter()
        d = dedup_detections(det(im), {})
        secs.append(time.perf_counter() - t)
        out.append(d)
    return out, secs


def compare(ref, got):
    matched, ious, kpd, dconf, missed, extra = 0, [], [], [], 0, 0
    for r_img, g_img in zip(ref, got):
        used = set()
        for r in r_img:
            best, bi = 0.0, None
            for i, g in enumerate(g_img):
                if i not in used and (o := iou(r["box"], g["box"])) > best:
                    best, bi = o, i
            if bi is None or best < 0.5:
                missed += 1
                continue
            used.add(bi)
            g = g_img[bi]
            matched += 1
            ious.append(best)
            dconf.append(g["conf"] - r["conf"])
            diag = np.hypot(r["box"][2] - r["box"][0], r["box"][3] - r["box"][1])
            both = [k for k in range(17) if r["kpc"][k] >= 0.5 and g["kpc"][k] >= 0.5]
            if both:
                kpd.append(100 * np.mean([np.hypot(r["kp"][k][0] - g["kp"][k][0], r["kp"][k][1] - g["kp"][k][1]) for k in both]) / diag)
        extra += len(g_img) - len(used)
    mean = lambda xs: round(float(np.mean(xs)), 4) if xs else None
    return {"matched": matched, "missed": missed, "extra": extra, "box_iou": mean(ious), "kp_err_pct": mean(kpd),
            "conf_delta": mean(dconf), "conf_delta_max": round(float(np.max(np.abs(dconf))), 4) if dconf else None}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("images", nargs="+", type=Path)
    ap.add_argument("--ref", default="yolo11n-pose.pt")
    ap.add_argument("--models", help="comma-separated; default: every installed model")
    ap.add_argument("--long-edge", type=int, default=1280)
    ap.add_argument("--json", type=Path)
    a = ap.parse_args()
    ims = [I.resize_long_edge(I.load(p, None)[0], a.long_edge) for p in a.images]
    ref, ref_secs = run(detectors.get(a.ref, a.long_edge), ims)
    names = a.models.split(",") if a.models else [m["name"] for m in detectors.installed()]
    report = {"reference": a.ref, "images": len(ims), "people": sum(map(len, ref)),
              "ref_s_per_img": round(statistics.median(ref_secs), 3), "device": detectors.device(), "models": {}}
    print(f"reference {a.ref}: {report['people']} people in {len(ims)} images, {report['ref_s_per_img']} s/img")
    print(f"{'model':16s} {'found':>5s} {'match':>5s} {'miss':>4s} {'extra':>5s} {'boxIoU':>7s} {'kp err%':>7s} {'dconf':>8s} {'s/img':>6s}")
    for n in names:
        got, secs = run(detectors.get(n, a.long_edge), ims)
        c = compare(ref, got)
        c.update(found=sum(map(len, got)), s_per_img=round(statistics.median(secs), 3))
        report["models"][n] = c
        print(f"{n:16s} {c['found']:5d} {c['matched']:5d} {c['missed']:4d} {c['extra']:5d} {c['box_iou'] or 0:7.4f} "
              f"{c['kp_err_pct'] or 0:7.3f} {c['conf_delta'] or 0:8.4f} {c['s_per_img']:6.3f}")
    if a.json:
        a.json.write_text(json.dumps(report, indent=1))


if __name__ == "__main__":
    main()
