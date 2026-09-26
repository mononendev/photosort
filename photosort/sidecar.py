"""Read the photographer's own verdicts from existing Lightroom/Bridge XMP sidecars (rating, label)."""
from __future__ import annotations
import os
import re
from pathlib import Path
from typing import Optional

_RATING = re.compile(r'xmp:Rating\s*=\s*"(-?\d)"|<xmp:Rating>\s*(-?\d)\s*</xmp:Rating>')
_LABEL = re.compile(r'xmp:Label\s*=\s*"([^"]*)"|<xmp:Label>\s*([^<]*?)\s*</xmp:Label>')


def rating_label(text: str) -> tuple[Optional[int], Optional[str]]:
    """xmp:Rating and xmp:Label from XMP text, in attribute or element form."""
    m = _RATING.search(text)
    rating = int(next(g for g in m.groups() if g is not None)) if m else None
    m = _LABEL.search(text)
    label = next((g for g in m.groups() if g is not None), None) if m else None
    return rating, label or None


def sidecar_for(path: Path, siblings: Optional[set[str]] = None) -> Optional[Path]:
    """The .xmp/.XMP next to `path`. `siblings`, the names in its folder, saves a stat per candidate."""
    for cand in (path.with_suffix(".xmp"), path.with_suffix(".XMP")):
        if cand.name in siblings if siblings is not None else cand.exists():
            return cand
    return None


def read_sidecar(path: Path, siblings: Optional[set[str]] = None) -> dict:
    """Returns {} when there is no sidecar; otherwise {"rating": int|None, "label": str|None, "sidecar": name}."""
    sc = sidecar_for(path, siblings)
    if sc is None:
        return {}
    try:
        text = sc.read_text(errors="ignore")
    except OSError:
        return {}
    rating, label = rating_label(text)
    return {"rating": rating, "label": label, "sidecar": sc.name}


def ingest(db, rows) -> int:
    """Store sidecar verdicts for rows whose lr_json is NULL. Returns how many had a sidecar."""
    listings: dict[Path, set[str]] = {}

    def siblings(folder: Path) -> set[str]:   # one directory listing per folder instead of two stats per image
        if folder not in listings:
            try:
                listings[folder] = set(os.listdir(folder))
            except OSError:
                listings[folder] = set()
        return listings[folder]
    found = [(r["id"], read_sidecar(p, siblings(p.parent))) for r in rows for p in [Path(r["path"])]]
    db.set_lr_many(found)
    return sum(1 for _, d in found if d)
