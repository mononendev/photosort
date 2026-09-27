"""Compare the `local` sections of two golden fixture sets: how far a dependency or detector change moved the stored
numbers, and whether any tier changed.  python analyzer/scripts/drift.py OLD_DIR NEW_DIR"""
import json
import sys
from pathlib import Path


def walk(a, b, path, out):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(a.keys() | b.keys()):
            walk(a.get(k), b.get(k), f"{path}.{k}", out)
    elif isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
        for i, (x, y) in enumerate(zip(a, b)):
            walk(x, y, f"{path}[{i}]", out)
    elif isinstance(a, (int, float)) and isinstance(b, (int, float)) and not isinstance(a, bool):
        if a != b:
            out.append((path, a, b, abs(a - b) / max(abs(a), abs(b), 1e-12)))
    elif a != b:
        out.append((path, a, b, None))


def main():
    old, new = Path(sys.argv[1]), Path(sys.argv[2])
    worst = 0.0
    for f in sorted(old.glob("*.json")):
        g = new / f.name
        if not g.exists():
            continue
        a, b = json.loads(f.read_text())["local"], json.loads(g.read_text())["local"]
        out = []
        walk(a, b, "", out)
        tiers = (a["local_tier"], b["local_tier"])
        print(f"{f.name}: tier {tiers[0]}->{tiers[1]}, {len(out)} values differ")
        for p, x, y, rel in sorted(out, key=lambda t: -(t[3] or 1))[:5]:
            print(f"   {p}: {x} -> {y}" + (f"  ({rel:.2e})" if rel is not None else ""))
            worst = max(worst, rel or 1)
    print(f"worst relative change: {worst:.2e}")


if __name__ == "__main__":
    main()
