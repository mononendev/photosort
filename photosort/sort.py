"""Stage 3: merge results, build the sorted link tree, export CSV/JSONL and XMP sidecars."""
from __future__ import annotations
import csv
import json
import os
import shutil
from pathlib import Path
from xml.sax.saxutils import escape

from .db import jcol

TIER_NAMES = {0: "focus_0_miss", 1: "focus_1_partial", 2: "focus_2_soft", 3: "focus_3_sharp"}
# Your cull rating from the UI: 0-3 are the focus tiers, 4 is a banger (sharp and a favorite; only you give it).
# Exported as the matching color label. Lightroom's stock label set has no Orange; it shows as a custom label.
BANGER = 4
RATING_LABELS = {0: "Red", 1: "Orange", 2: "Yellow", 3: "Green", 4: "Blue"}


def final_record(row, source: str) -> dict:
    local, vlm = jcol(row, "local_json"), jcol(row, "vlm_json")
    ov = jcol(row, "override_json", {}) if "override_json" in row.keys() else {}
    lt = local["local_tier"] if local else None
    vt = vlm["focus_tier"] if vlm else None
    if source == "local" or vt is None:
        tier = lt
    elif source == "strict" and lt is not None:
        tier = min(lt, vt)
    else:
        tier = vt
    if ov.get("focus_tier") is not None:
        tier = int(ov["focus_tier"])
    rec = {
        "path": row["path"],
        "focus_tier": tier,
        "focus_tier_local": lt, "focus_tier_vlm": vt,
        "review": (lt is not None and vt is not None and lt != vt),
        "subject": (vlm or {}).get("primary_subject", "no_people" if (local and local["n_people"] == 0) else "unknown"),
        "composition": (vlm or {}).get("composition", "unknown"),
        "placement": (vlm or {}).get("subject_placement"),
        "action": (vlm or {}).get("action"),
        "people_count": (vlm or {}).get("people_count", local["n_people"] if local else None),
        "keywords": (vlm or {}).get("keywords", []),
        "adjectives": (vlm or {}).get("adjectives", []),
        "description": (vlm or {}).get("description"),
        "focus_notes": (vlm or {}).get("focus_notes"),
        "quality_remarks": (vlm or {}).get("quality_remarks"),
        "quality_score": ov.get("quality_score", (vlm or {}).get("quality_score")),
        "keeper": ov.get("keeper", (vlm or {}).get("keeper")),
        "note": ov.get("note"),
        "rating": ov.get("rating"),
        "banger": ov.get("rating") == BANGER,
        "reviewed": bool(ov.get("reviewed")),
        "overridden": bool(ov),
        "local": {k: local.get(k) for k in ("n_people", "primary_head_sharp", "primary_body_sharp", "bg_sharp", "local_reason")} if local else None,
        "error": row["error"],
    }
    return rec


def place(src: Path, dst: Path, mode: str):
    dst.parent.mkdir(parents=True, exist_ok=True)
    if dst.exists() or dst.is_symlink():
        dst.unlink()
    if mode == "symlink":
        os.symlink(src.resolve(), dst)
    elif mode == "hardlink":
        os.link(src, dst)
    elif mode == "copy":
        shutil.copy2(src, dst)
    elif mode == "move":
        shutil.move(str(src), dst)


def build_tree(records: list[dict], out: Path, mode: str) -> dict:
    counts: dict[str, int] = {}
    for r in records:
        src = Path(r["path"])
        if not src.exists() or r["focus_tier"] is None:
            continue
        tier_dir = TIER_NAMES[r["focus_tier"]]
        if r["subject"] == "unknown" and r["composition"] == "unknown":
            dst = out / tier_dir / src.name                      # local-only run: no subject info yet
        else:
            dst = out / tier_dir / r["subject"] / r["composition"] / src.name
        place(src, dst, mode)
        counts[tier_dir] = counts.get(tier_dir, 0) + 1
        if r["review"]:
            place(src, out / "review" / f"local{r['focus_tier_local']}_vlm{r['focus_tier_vlm']}" / src.name, "symlink" if mode == "move" else mode)
            counts["review"] = counts.get("review", 0) + 1
        if r.get("banger"):
            place(src, out / "bangers" / src.name, "symlink" if mode == "move" else mode)
            counts["bangers"] = counts.get("bangers", 0) + 1
    return counts


def export(records: list[dict], out: Path):
    out.mkdir(parents=True, exist_ok=True)
    with (out / "results.jsonl").open("w") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")
    cols = ["path", "focus_tier", "focus_tier_local", "focus_tier_vlm", "review", "subject", "composition", "placement",
            "action", "people_count", "rating", "reviewed", "quality_score", "keeper", "keywords", "adjectives", "description", "focus_notes",
            "quality_remarks", "error"]
    with (out / "results.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=cols, extrasaction="ignore")
        w.writeheader()
        for r in records:
            row = dict(r)
            row["keywords"] = "; ".join(r["keywords"] or [])
            row["adjectives"] = "; ".join(r["adjectives"] or [])
            w.writerow(row)


XMP_TMPL = """<?xpacket begin="﻿" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="photosort">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:dc="http://purl.org/dc/elements/1.1/"
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:lr="http://ns.adobe.com/lightroom/1.0/"
    xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"
    xmlns:photosort="http://photosort.local/ns/1.0/"
    {rating}
    {label}
    {attrs}>
   <dc:subject><rdf:Bag>
{subjects}
   </rdf:Bag></dc:subject>
   <lr:hierarchicalSubject><rdf:Bag>
{hier}
   </rdf:Bag></lr:hierarchicalSubject>
   <dc:description><rdf:Alt><rdf:li xml:lang="x-default">{description}</rdf:li></rdf:Alt></dc:description>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>
"""


def xmp_for(r: dict) -> str:
    tier = r["focus_tier"]
    kws = list(r["keywords"] or []) + list(r["adjectives"] or [])
    tags = [f"focus-{TIER_NAMES.get(tier, 'unknown')}", f"subject-{r['subject']}", f"comp-{r['composition']}"]
    if r["action"] and r["action"] != "none":
        tags.append(f"action-{r['action']}")
    hier = [f"PhotoSort|Focus|{TIER_NAMES.get(tier, 'unknown')}", f"PhotoSort|Subject|{r['subject']}",
            f"PhotoSort|Composition|{r['composition']}"]
    if r["review"]:
        tags.append("photosort-review")
        hier.append("PhotoSort|Review")
    if r.get("banger"):
        tags.append("photosort-banger")
        hier.append("PhotoSort|Banger")
    li = lambda xs: "\n".join(f"    <rdf:li>{escape(str(x))}</rdf:li>" for x in xs)
    instr = " ".join(x for x in (r["focus_notes"], r["quality_remarks"]) if x)
    attrs = {
        "photosort:FocusTier": tier, "photosort:FocusTierLocal": r["focus_tier_local"], "photosort:FocusTierVLM": r["focus_tier_vlm"],
        "photosort:QualityScore": r.get("quality_score"),
        "photosort:Rating": r.get("rating"),
        "photosort:Keeper": None if r.get("keeper") is None else str(r["keeper"]).lower(),
        "photoshop:Instructions": instr or None,
    }
    attr_s = "\n    ".join(f'{k}="{escape(str(v), {chr(34): "&quot;"})}"' for k, v in attrs.items() if v is not None)
    return XMP_TMPL.format(
        rating=f'xmp:Rating="{r["quality_score"]}"' if r.get("quality_score") else "",
        label=f'xmp:Label="{RATING_LABELS[r["rating"]]}"' if r.get("rating") in RATING_LABELS else "",
        attrs=attr_s, subjects=li(kws + tags), hier=li(hier),
        description=escape(r["description"] or ""))


def write_xmp(records: list[dict], xmp_dir: Path | None, overwrite: bool) -> tuple[int, int]:
    written = skipped = 0
    for r in records:
        src = Path(r["path"])
        if r["focus_tier"] is None:
            continue
        dst = (xmp_dir / (src.stem + ".xmp")) if xmp_dir else src.with_suffix(".xmp")
        if dst.exists() and not overwrite:
            skipped += 1
            continue
        dst.parent.mkdir(parents=True, exist_ok=True)
        dst.write_text(xmp_for(r), encoding="utf-8")
        written += 1
    return written, skipped
