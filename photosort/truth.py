"""Ground truth for calibration: the photographer's own verdicts, exported from Lightroom/Bridge as
XMP sidecars (Save Metadata to File), a metadata XML, or a CSV. Matched to images by filename stem.

A verdict is {"rating": 0-5|None, "label": str|None, "focus_tier": 0|1|2|None, "keywords": [...]} .
focus_tier is taken from, in order: an explicit CSV column; a keyword like "focus2" / "focus:1" /
"tier0"; the color label via cfg["truth"]["label_tiers"]; the star rating via cfg["truth"]["rating_tiers"].
"""
from __future__ import annotations
import csv
import io
import re
import zipfile
from pathlib import Path
from typing import Iterable, Optional

from .sidecar import _LABEL, _RATING

_SUBJECT = re.compile(r"<dc:subject>(.*?)</dc:subject>", re.S)
_LI = re.compile(r"<rdf:li[^>]*>(.*?)</rdf:li>", re.S)
_FOCUS_KW = re.compile(r"^(?:focus|tier)[:_ -]?([012])$", re.I)
DEFAULT_TRUTH_CFG = {
    "label_tiers": {"Green": 2, "Yellow": 1, "Red": 0},
    "rating_tiers": {"5": 2, "4": 2, "3": 1, "2": 1, "1": 0, "0": None},
}


def parse_xmp(text: str) -> dict:
    m = _RATING.search(text)
    rating = int(next(g for g in m.groups() if g is not None)) if m else None
    m = _LABEL.search(text)
    label = next((g for g in m.groups() if g is not None), None) if m else None
    kws: list[str] = []
    for block in _SUBJECT.findall(text):
        kws += [k.strip() for k in _LI.findall(block) if k.strip()]
    return {"rating": rating, "label": label or None, "keywords": kws, "focus_tier": None}


def parse_csv(text: str) -> dict[str, dict]:
    """Columns (case-insensitive): name|file|path (required), rating, label, focus_tier|tier, keywords."""
    out = {}
    rd = csv.DictReader(io.StringIO(text))
    for row in rd:
        r = {k.strip().lower(): (v or "").strip() for k, v in row.items() if k}
        name = r.get("name") or r.get("file") or r.get("path") or r.get("filename")
        if not name:
            continue
        stem = Path(name).stem.lower()
        tier = r.get("focus_tier") or r.get("tier")
        out[stem] = {
            "rating": int(r["rating"]) if r.get("rating", "").lstrip("-").isdigit() else None,
            "label": r.get("label") or None,
            "focus_tier": int(tier) if tier in ("0", "1", "2") else None,
            "keywords": [k.strip() for k in (r.get("keywords") or "").replace(";", ",").split(",") if k.strip()],
        }
    return out


def parse_files(files: Iterable[tuple[str, bytes]]) -> dict[str, dict]:
    """(filename, bytes) pairs -> {stem: verdict}. Zips are expanded; .xmp/.xml parsed as XMP; .csv as CSV."""
    out: dict[str, dict] = {}
    for name, data in files:
        low = name.lower()
        if low.endswith(".zip"):
            with zipfile.ZipFile(io.BytesIO(data)) as z:
                out.update(parse_files((i.filename, z.read(i)) for i in z.infolist() if not i.is_dir()))
        elif low.endswith(".csv"):
            out.update(parse_csv(data.decode("utf-8", "ignore")))
        elif low.endswith((".xmp", ".xml")):
            out[Path(name).stem.lower()] = parse_xmp(data.decode("utf-8", "ignore"))
    return out


def load_dir(d: Path) -> dict[str, dict]:
    files = [(p.name, p.read_bytes()) for p in d.rglob("*") if p.is_file() and p.suffix.lower() in (".xmp", ".xml", ".csv", ".zip")]
    return parse_files(files)


def resolve_tier(v: dict, cfg: dict) -> Optional[int]:
    if v.get("focus_tier") in (0, 1, 2):
        return v["focus_tier"]
    for k in v.get("keywords") or []:
        m = _FOCUS_KW.match(k)
        if m:
            return int(m.group(1))
    t = cfg.get("truth", DEFAULT_TRUTH_CFG)
    if v.get("label") and v["label"] in t.get("label_tiers", {}):
        return t["label_tiers"][v["label"]]
    if v.get("rating") is not None:
        r = t.get("rating_tiers", {}).get(str(v["rating"]))
        return r if r in (0, 1, 2) else None
    return None


def apply(db, verdicts: dict[str, dict], cfg: dict, folder: Optional[str] = None) -> dict:
    """Attach verdicts to tracked images by filename stem. Returns counts."""
    where, params = "1", []
    if folder:
        where, params = "(folder = ? OR folder LIKE ?)", [folder, folder.rstrip("/") + "/%"]
    matched = 0
    seen = set()
    for r in db.rows(where, params):
        stem = Path(r["path"]).stem.lower()
        v = verdicts.get(stem)
        if v is None:
            continue
        v = dict(v)
        v["focus_tier"] = resolve_tier(v, cfg)
        db.set_truth(r["id"], v)
        matched += 1
        seen.add(stem)
    return {"verdicts": len(verdicts), "matched": matched, "unmatched": len(set(verdicts) - seen)}


def suggest_thresholds(pairs: list[tuple[float, int]]) -> dict:
    """pairs of (head_sharpness, truth_tier). Grid-search thresholds maximizing balanced accuracy for
    tier2-vs-rest and tier0-vs-rest. Returns {} when there isn't enough of each class."""
    import numpy as np
    if len(pairs) < 10:
        return {}
    x = np.array([p[0] for p in pairs]); y = np.array([p[1] for p in pairs])
    cands = np.unique(np.quantile(x, np.linspace(0.02, 0.98, 97)))

    def best(pos_mask):
        if pos_mask.sum() < 3 or (~pos_mask).sum() < 3:
            return None
        scores = [((x[pos_mask] >= c).mean() + (x[~pos_mask] < c).mean()) / 2 for c in cands]
        i = int(np.argmax(scores))
        return {"value": round(float(cands[i]), 4), "balanced_accuracy": round(float(scores[i]), 3)}

    t2 = best(y == 2)
    t1 = best(y >= 1)
    out = {}
    if t2:
        out["tier2_min"] = t2
    if t1:
        out["tier1_min"] = t1
    return out
