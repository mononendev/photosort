"""Compare local_json between two workdirs' databases, image by image (matched by path).

  python3 scripts/apidiff/compare_local.py OLD.db NEW.db [--tol 1e-5]

Numbers compare within a relative tolerance (detector confidences move in the 6th digit between ultralytics
releases); everything else exactly. Keys only the new side has (e.g. "detector") are listed, not failed.
"""
import argparse
import json
import sqlite3
import sys


def walk(a, b, path, tol, out):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(set(a) | set(b)):
            if k not in b:
                out.append(f"{path}.{k} missing")
            elif k in a:
                walk(a[k], b[k], f"{path}.{k}", tol, out)
    elif isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
        for i, (x, y) in enumerate(zip(a, b)):
            walk(x, y, f"{path}[{i}]", tol, out)
    elif isinstance(a, (int, float)) and isinstance(b, (int, float)) and not isinstance(a, bool):
        if abs(a - b) > tol * max(1.0, abs(a), abs(b)):
            out.append(f"{path}: {a} vs {b}")
    elif a != b:
        out.append(f"{path}: {a!r:.100} vs {b!r:.100}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("old")
    ap.add_argument("new")
    ap.add_argument("--tol", type=float, default=1e-5)
    a = ap.parse_args()
    load = lambda p: {r[0]: json.loads(r[1]) for r in sqlite3.connect(p).execute(
        "SELECT path, local_json FROM images WHERE local_json IS NOT NULL")}
    old, new = load(a.old), load(a.new)
    bad = 0
    for path in sorted(old):
        if path not in new:
            print(f"MISSING {path}")
            bad += 1
            continue
        out = []
        walk(old[path], new[path], "", a.tol, out)
        extra = sorted(set(new[path]) - set(old[path]))
        tag = "DIFF" if out else "ok  "
        print(f"{tag} {path.rsplit('/', 1)[-1]}: tier {old[path]['local_tier']} -> {new[path]['local_tier']}"
              + (f"  (new keys: {', '.join(extra)})" if extra else ""))
        for o in out[:8]:
            print("     ", o)
        bad += bool(out)
    print(f"{bad} image(s) differ")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
