"""Read the photographer's own verdicts from existing Lightroom/Bridge XMP sidecars (rating, label)."""
from __future__ import annotations
import re
from pathlib import Path
from typing import Optional

_RATING = re.compile(r'xmp:Rating\s*=\s*"(-?\d)"|<xmp:Rating>\s*(-?\d)\s*</xmp:Rating>')
_LABEL = re.compile(r'xmp:Label\s*=\s*"([^"]*)"|<xmp:Label>\s*([^<]*?)\s*</xmp:Label>')


def sidecar_for(path: Path) -> Optional[Path]:
    for cand in (path.with_suffix(".xmp"), path.with_suffix(".XMP")):
        if cand.exists():
            return cand
    return None


def read_sidecar(path: Path) -> dict:
    """Returns {} when there is no sidecar; otherwise {"rating": int|None, "label": str|None, "sidecar": name}."""
    sc = sidecar_for(path)
    if sc is None:
        return {}
    try:
        text = sc.read_text(errors="ignore")
    except OSError:
        return {}
    m = _RATING.search(text)
    rating = int(next(g for g in m.groups() if g is not None)) if m else None
    m = _LABEL.search(text)
    label = next((g for g in m.groups() if g is not None), None) if m else None
    return {"rating": rating, "label": label or None, "sidecar": sc.name}


def ingest(db, rows) -> int:
    """Store sidecar verdicts for rows whose lr_json is NULL. Returns how many had a sidecar."""
    n = 0
    for r in rows:
        d = read_sidecar(Path(r["path"]))
        db.set_lr(r["id"], d)
        n += bool(d)
    return n
