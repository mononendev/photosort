"""Serve one fixture workdir from the Python and the Go backend and diff every route the UI uses.

  PHOTOSORT_LEGACY=../photosort-py python3 scripts/apidiff/diff.py FIXTURE_DIR [--py PYTHON] [--pg postgres://...]

The old Python package isn't in this tree any more: point PHOTOSORT_LEGACY at a checkout that has it
(git worktree add ../photosort-py a8ba797), and use that checkout's interpreter.

FIXTURE_DIR comes from build.py. The Go server gets a copy of the workdir (or, with --pg, the same data copied into
that Postgres database with `photosort db copy`). Mutations (PATCH, PUT config, rescore, export, truth) go to both,
and their answers and the state after them are compared too. Exit status 1 on any difference.
"""
import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# Values that differ by nature: build and process details, clocks, and where each server's workdir is.
IGNORE = {"version", "workdir", "models_dir", "device", "database", "analyzer", "now", "elapsed", "rate", "eta_s",
          "out", "mtime", "heartbeat"}


import re

# Known, accepted differences (route pattern, difference pattern), each with its reason:
KNOWN = [
    # Go shrinks the Ollama frame with Catmull-Rom (Pillow's LANCZOS has no Go equivalent): same size, other bytes.
    (r"vlm-request\?backend=ollama", r"\.request\.messages\[1\]\.images\[0\]: '<base64 image"),
    # The analyzer runs numpy 2.5 (the Python server 2.2): float32 sums differ in the 7th digit, and the debug JPEGs
    # built from them differ in a few pixels. The stored metrics are rounded well above that.
    (r"/focus-debug", r"\.(gray_var|lap_var|value|band_energy|total_energy|energy\[\d+\]): [-\d.e]+ vs [-\d.e]+$"),
    (r"/focus-debug", r"\.img: 'data:image/"),
    # Validation errors: a readable string instead of pydantic's error list (the UI shows either as text).
    (r"^PATCH \{'rating': 9\}", r"\.detail: "),
    # Added with selectable pose models: the NMS overlap setting, the new default model, and the stats' detector
    # fields. Additions only; nothing the Python server returned changed.
    (r"/api/config", r"\.detect_iou: extra in go"),
    (r"/api/config/defaults", r"\.detect_model: 'yolo11n-pose\.pt' vs 'yolo26s-pose'"),
    (r"/api/stats", r"\.detector(_stale)?: extra in go"),
]


def known(name, problem):
    return any(re.search(r, name) and re.search(p, problem) for r, p in KNOWN)


def get(base, path, method="GET", body=None, raw=False, form=None):
    data, headers = None, {}
    if body is not None:
        data, headers = json.dumps(body).encode(), {"Content-Type": "application/json"}
    if form is not None:
        boundary = "apidiffboundary"
        parts = []
        for name, (fname, content) in form:
            parts.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"; filename=\"{fname}\"\r\n"
                         f"Content-Type: application/octet-stream\r\n\r\n".encode() + content + b"\r\n")
        data = b"".join(parts) + f"--{boundary}--\r\n".encode()
        headers = {"Content-Type": f"multipart/form-data; boundary={boundary}"}
    req = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=300) as r:
            b = r.read()
            return r.status, (b if raw else json.loads(b or b"null"))
    except urllib.error.HTTPError as e:
        b = e.read()
        try:
            return e.code, json.loads(b)
        except ValueError:
            return e.code, b.decode(errors="replace")


def diff(a, b, path=""):
    if isinstance(a, dict) and isinstance(b, dict):
        out = []
        for k in sorted(set(a) | set(b)):
            if k in IGNORE:
                continue
            if k not in a or k not in b:
                out.append(f"{path}.{k}: {'missing in go' if k in a else 'extra in go'} ({a.get(k, b.get(k))!r:.80})")
            else:
                out += diff(a[k], b[k], f"{path}.{k}")
        return out
    if isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            return [f"{path}: {len(a)} items vs {len(b)}"]
        return [d for i, (x, y) in enumerate(zip(a, b)) for d in diff(x, y, f"{path}[{i}]")]
    if isinstance(a, bool) or isinstance(b, bool):
        return [] if a is b else [f"{path}: {a!r} vs {b!r}"]
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return [] if abs(a - b) <= 1e-9 * max(1, abs(a), abs(b)) else [f"{path}: {a!r} vs {b!r}"]
    return [] if a == b else [f"{path}: {a!r:.200} vs {b!r:.200}"]


def wait(base, proc, name):
    for _ in range(240):
        if proc.poll() is not None:
            sys.exit(f"{name} server exited")
        try:
            if get(base, "/api/health")[0] == 200:
                return
        except OSError:
            pass
        time.sleep(0.5)
    sys.exit(f"{name} server never answered")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("fixture", type=Path)
    legacy = Path(os.environ.get("PHOTOSORT_LEGACY", ROOT))
    ap.add_argument("--py", default=str(legacy / ".venv/bin/python"), help="the old package's interpreter")
    ap.add_argument("--pg", help="run the Go server on this Postgres database (emptied first)")
    ap.add_argument("--no-mutate", action="store_true")
    a = ap.parse_args()
    fx = a.fixture.resolve()
    photos = fx / "photos"
    pywork, gowork = fx / "work-py", fx / "work-go"
    for d in (pywork, gowork):
        shutil.rmtree(d, ignore_errors=True)
        shutil.copytree(fx / "work", d)
    env = {**os.environ, "PHOTOSORT_MODELS": str(ROOT / ".models")}
    goenv = {**env, "PHOTOSORT_ANALYZER_DIR": str(ROOT / "analyzer")}
    gobin = fx / "photosort"
    subprocess.run(["go", "build", "-o", str(gobin), "./cmd/photosort"], cwd=ROOT, check=True)
    if a.pg:
        goenv["PHOTOSORT_DB"] = a.pg
        subprocess.run([str(gobin), "db", "copy", "--from", f"sqlite://{gowork / 'photosort.db'}", "--to", a.pg],
                       env=goenv, check=True)
    procs = [
        subprocess.Popen([a.py, "-m", "photosort", "--workdir", str(pywork), "web", "--photos", str(photos), "--port", "18080"],
                         cwd=legacy, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL),
        subprocess.Popen([str(gobin), "--workdir", str(gowork), "web", "--photos", str(photos), "--port", "18081"],
                         cwd=ROOT, env=goenv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL),
    ]
    PY, GO = "http://127.0.0.1:18080", "http://127.0.0.1:18081"
    failures = 0
    try:
        wait(PY, procs[0], "python")
        wait(GO, procs[1], "go")

        def check(path, method="GET", body=None, raw=False, form=None, label=None):
            nonlocal failures
            sa, ra = get(PY, path, method, body, raw, form)
            sb, rb = get(GO, path, method, body, raw, form)
            problems = [f"status {sa} vs {sb}"] if sa != sb else []
            if raw:
                problems += [] if ra == rb else [f"bytes differ ({len(ra)} vs {len(rb)})"]
            else:
                problems += diff(ra, rb)
            name = label or f"{method} {path}"
            accepted = [p for p in problems if known(name, p)]
            problems = [p for p in problems if not known(name, p)]
            if accepted and not problems:
                print(f"ok*  {name} ({len(accepted)} known differences)")
                return ra
            if problems:
                failures += 1
                print(f"DIFF {name}")
                for p in problems[:12]:
                    print("   ", p)
                if len(problems) > 12:
                    print(f"    ... {len(problems) - 12} more")
            else:
                print(f"ok   {name}")
            return ra

        check("/api/health")
        check("/api/stats")
        for p in ("", "day_1", "day_2", "day_2/a_b"):
            check(f"/api/tree?path={p}")
        check("/api/tree?path=../..")
        check("/api/tree?path=nope")
        jobs = check("/api/jobs")
        for j in jobs:
            check(f"/api/jobs/{j['id']}")
            check(f"/api/jobs/{j['id']}/detail")
            for q in ("", "?stage=local", "?stage=vlm", "?errors=true", "?limit=2&offset=1"):
                check(f"/api/jobs/{j['id']}/items{q}")
        check("/api/jobs/999")
        page = check("/api/images?limit=500")
        ids = [i["id"] for i in page["items"]]
        filters = ["tier=0", "tier=2", "keeper=true", "keeper=false", "status=tagged", "status=analyzed", "status=error",
                   "status=pending", "review=true", "split=true", "split=false", "lr_rating=5", "lr_label=Red",
                   "truth_tier=2", "truth_mismatch=true", "rating=2", "reviewed=true", "reviewed=false", "group=0",
                   "group=2", "local_tier=0", "vlm_tier=1", "stages=agree", "stages=disagree", "stale=true",
                   "stale=false", "composition=full_body", "eye_src=none", "eye_src=face", "primary_by=priority",
                   "lifted=true", "lifted=false", "overridden=true", "overridden=false", "noted=true", "noted=false",
                   "people_min=2", "people_max=1", "score_min=3", "score_max=2", "eye_min=0.01", "eye_max=0.05",
                   "iso_min=100", "f_max=4", "shutter_min=0.001", "focal_min=10", "taken_from=2020-01-01",
                   "taken_to=2030-12-31", "q=rider", "q=BUS", "folder=day_1", "folder=day_2&recursive=false",
                   "folder=day_2", "subject=rider_action", "camera=x", "lens=y"]
        for f in filters:
            check(f"/api/images?{f}")
        for s in ("path", "newest", "score", "score_low", "sharpness", "eye_sharpness", "eye_softest", "taken",
                  "taken_desc", "people", "iso", "name", "shuffle", "lr", "rated", "junk"):
            check(f"/api/images?sort={s}&limit=500")
        check("/api/images?limit=3&offset=2")
        check("/api/images/facets")
        for i in ids:
            check(f"/api/images/{i}")
            check(f"/api/images/{i}/trace")
            for b in ("ollama", "anthropic", "gemini"):
                check(f"/api/images/{i}/vlm-request?backend={b}")
            for m in ("thumb", "frame", "crop"):
                check(f"/media/{m}/{i}", raw=True)
        check(f"/api/images/{ids[0]}/focus-debug")
        check("/api/images/999")
        check("/api/images/999/vlm-request")
        check("/api/images/1/vlm-request?backend=nope")
        check("/api/config")
        check("/api/config/defaults")
        check("/api/config/history")
        for m in ("eye", "hf", "head", "nope"):
            check(f"/api/calibration?metric={m}")
        check("/api/calibration?n=3")
        for s in ("both", "ratings", "imported", "nope"):
            check(f"/api/truth?source={s}")
        check("/api/exports")

        if not a.no_mutate:
            i = ids[1]
            for body in ({"rating": 4}, {"quality_score": 2, "note": "hm", "keeper": False}, {"group": 3},
                         {"clear_group": True}, {"clear_score": True}, {"focus_tier": 1}, {"clear_rating": True},
                         {"rating": 9}, {"clear": True}):
                check(f"/api/images/{i}", "PATCH", body, label=f"PATCH {body}")
            check("/api/config", "PUT", {"values": {"focus": {"tier3_min": 0.02}, "focus_source": "strict"}, "source": "apidiff"})
            check("/api/stats")
            check("/api/images?tier=3")
            check("/api/rescore", "POST", {"source": "apidiff"})
            check("/api/images?limit=500&sort=eye_softest")
            hist = check("/api/config/history")
            check(f"/api/config/history/{hist[-1]['id']}/restore", "POST") if hist else None
            check("/api/config")
            check("/api/export", "POST", {"name": "e1", "folder": "day_1", "link": "copy"})
            check("/api/export", "POST", {"name": "e2", "xmp": False, "tree": False, "focus_source": "local"})
            csv = b"filename,rating,label\nzidane_sharp.jpg,5,Blue\nbus_sharp.jpg,1,Red\nnothere.jpg,3,\n"
            check("/api/truth/upload", "POST", form=[("files", ("verdicts.csv", csv))])
            check("/api/truth?source=imported")
            check("/api/truth", "DELETE")
            check("/api/jobs/1/override", "POST")
            check(f"/api/jobs/{jobs[0]['id']}/cancel", "POST")
    finally:
        for p in procs:
            p.send_signal(signal.SIGINT)
        for p in procs:
            try:
                p.wait(10)
            except subprocess.TimeoutExpired:
                p.kill()
    print(f"\n{failures} route(s) differ")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
