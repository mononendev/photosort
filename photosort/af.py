"""Where the camera was asked to focus, read from the maker notes.

The AF points say what the photographer (or the camera's auto selection) meant to be the
subject, independent of whether focus landed. The local stage uses them to pick the primary
person, and the detail view draws them over the frame.

Canon stores the AF layout in MakerNote tag 0x0026 (AFInfo2; 0x003C AFInfo3 on newer bodies)
as int16 words, the same layout ExifTool documents:

    0 size (bytes), 1 AFAreaMode, 2 NumAFPoints (N), 3 ValidAFPoints,
    4-5 CanonImageWidth/Height, 6-7 AFImageWidth/Height,
    then N widths, N heights, N x centers, N y centers,
    then ceil(N/16) words of AFPointsInFocus bits, ceil(N/16) words of AFPointsSelected bits,
    then PrimaryAFPoint on some bodies.

Point centers are relative to the image center in AFImageWidth/Height units, x to the right,
y upward (Cartesian). They are in sensor orientation, so they are rotated by the EXIF
orientation to match the upright frame the rest of the pipeline sees.

exifread parses the Canon maker note but doesn't name these tags; they come through as
"MakerNote Tag 0x0026". Files exifread can't read (CR3) fall back to exiftool when installed.
"""
from __future__ import annotations
import json
import shutil
import subprocess
from pathlib import Path
from typing import Optional

AREA_MODES = {
    0: "manual focus", 1: "AF point expansion (surround)", 2: "single-point", 4: "auto (multi-point)",
    5: "face detect", 6: "face + tracking", 7: "zone", 8: "AF point expansion (4 point)", 9: "spot",
    10: "AF point expansion (8 point)", 11: "flexizone multi (49 point)", 12: "flexizone multi (9 point)",
    13: "flexizone single", 14: "large zone", 16: "large zone (vertical)", 17: "large zone (horizontal)",
    19: "flexible zone 1", 20: "flexible zone 2", 21: "flexible zone 3", 22: "whole area", 23: "whole area (tracking)",
}
# Modes where the photographer placed the point(s); in the others the camera chose.
USER_PLACED = {1, 2, 8, 9, 10, 13}


def _s16(v: int) -> int:
    v = int(v) & 0xFFFF
    return v - 0x10000 if v >= 0x8000 else v


def _bits(words: list[int], n: int) -> list[bool]:
    return [bool((int(words[i // 16]) >> (i % 16)) & 1) if i // 16 < len(words) else False for i in range(n)]


def parse_afinfo2(words: list[int]) -> Optional[dict]:
    """Decode the raw AFInfo2/AFInfo3 word array into sensor-frame points. None if malformed."""
    w = [_s16(v) for v in words]
    if len(w) < 8:
        return None
    n = w[2]
    nw = (n + 15) // 16
    if n <= 0 or len(w) < 8 + 4 * n + nw:
        return None
    aw, ah = w[6] & 0xFFFF, w[7] & 0xFFFF
    if not aw or not ah:
        return None
    o = 8
    widths, heights = w[o:o + n], w[o + n:o + 2 * n]
    xs, ys = w[o + 2 * n:o + 3 * n], w[o + 3 * n:o + 4 * n]
    o += 4 * n
    in_focus = _bits(w[o:o + nw], n)
    o += nw
    selected = _bits(w[o:o + nw], n) if len(w) >= o + nw else [False] * n
    o += nw
    primary = w[o] if len(w) > o and 0 <= w[o] < n else None
    valid = w[3] if 0 < w[3] <= n else n
    pts = []
    for i in range(valid):
        if widths[i] <= 0 or heights[i] <= 0:
            continue
        pts.append({"i": i, "x": xs[i], "y": ys[i], "w": widths[i], "h": heights[i],
                    "in_focus": in_focus[i], "selected": selected[i]})
    return {"mode": w[1], "af_size": [aw, ah], "points": pts, "primary_point": primary}


def _orient(box: tuple, W: int, H: int, orientation: int) -> tuple:
    """Rotate a sensor-frame box (sensor W x H) into the upright frame."""
    x0, y0, x1, y1 = box
    if orientation == 3:
        return W - x1, H - y1, W - x0, H - y0
    if orientation == 6:  # rotate 90 CW
        return H - y1, x0, H - y0, x1
    if orientation == 8:  # rotate 90 CCW
        return y0, W - x1, y1, W - x0
    return box


def to_frame(raw: dict, W: int, H: int, orientation: int = 1, y_up: bool = True) -> dict:
    """Place decoded points on the upright W x H frame as pixel boxes."""
    sw, sh = (H, W) if orientation in (6, 8) else (W, H)  # sensor-orientation size of this frame
    aw, ah = raw["af_size"]
    kx, ky = sw / aw, sh / ah
    pts = []
    for p in raw["points"]:
        cx = sw / 2 + p["x"] * kx
        cy = sh / 2 + (-p["y"] if y_up else p["y"]) * ky
        hw, hh = p["w"] * kx / 2, p["h"] * ky / 2
        b = _orient((cx - hw, cy - hh, cx + hw, cy + hh), sw, sh, orientation)
        pts.append({"i": p["i"], "box": [round(v) for v in b], "in_focus": p["in_focus"], "selected": p["selected"]})
    mode = raw["mode"]
    active = [p for p in pts if p["in_focus"]] or [p for p in pts if p["selected"]]
    return {
        "source": raw.get("source", "canon"),
        "mode": mode, "mode_name": AREA_MODES.get(mode, f"mode {mode}"), "user_placed": mode in USER_PLACED,
        "n_points": len(pts), "primary_point": raw.get("primary_point"),
        # Only the points worth drawing: every one on a 61-point body is noise on the overlay.
        "points": [p for p in pts if p["in_focus"] or p["selected"]],
        "active": [p["i"] for p in active],
        "active_from": "in_focus" if any(p["in_focus"] for p in pts) else ("selected" if active else None),
    }


def _read_exifread(path: Path) -> tuple[Optional[list[int]], int]:
    try:
        import exifread
        with open(path, "rb") as f:
            tags = exifread.process_file(f, details=True, extract_thumbnail=False)
    except Exception:
        return None, 1
    ori = tags.get("Image Orientation")
    try:
        orientation = int(ori.values[0]) if ori is not None else 1
    except (AttributeError, IndexError, TypeError, ValueError):
        orientation = 1
    for tag in ("MakerNote Tag 0x0026", "MakerNote Tag 0x003C"):
        t = tags.get(tag)
        if t is not None and getattr(t, "values", None):
            return list(t.values), orientation
    return None, orientation


def _read_exiftool(path: Path) -> tuple[Optional[dict], int]:
    exe = shutil.which("exiftool")
    if not exe:
        return None, 1
    try:
        out = subprocess.run([exe, "-j", "-n", "-Orientation", "-AFAreaMode", "-NumAFPoints", "-ValidAFPoints",
                              "-AFImageWidth", "-AFImageHeight", "-AFAreaWidths", "-AFAreaHeights",
                              "-AFAreaXPositions", "-AFAreaYPositions", "-AFPointsInFocus", "-AFPointsSelected",
                              "-PrimaryAFPoint", str(path)], capture_output=True, text=True, timeout=20)
        d = json.loads(out.stdout)[0]
    except Exception:
        return None, 1
    orientation = int(d.get("Orientation") or 1)

    def arr(k):
        v = d.get(k)
        return [int(x) for x in str(v).split()] if v not in (None, "") else []

    def idx(k):  # exiftool -n prints point lists as comma-separated indices
        v = d.get(k)
        return {int(x) for x in str(v).replace(",", " ").split()} if v not in (None, "") else set()

    ws, hs, xs, ys = arr("AFAreaWidths"), arr("AFAreaHeights"), arr("AFAreaXPositions"), arr("AFAreaYPositions")
    n = min(len(ws), len(hs), len(xs), len(ys))
    if not n or not d.get("AFImageWidth") or not d.get("AFImageHeight"):
        return None, orientation
    foc, sel = idx("AFPointsInFocus"), idx("AFPointsSelected")
    pts = [{"i": i, "x": xs[i], "y": ys[i], "w": ws[i], "h": hs[i], "in_focus": i in foc, "selected": i in sel}
           for i in range(n) if ws[i] > 0 and hs[i] > 0]
    prim = d.get("PrimaryAFPoint")
    return {"mode": int(d.get("AFAreaMode") or -1), "af_size": [int(d["AFImageWidth"]), int(d["AFImageHeight"])],
            "points": pts, "primary_point": int(prim) if isinstance(prim, (int, float)) else None,
            "source": "exiftool"}, orientation


def read(path: Path, W: int, H: int, y_up: bool = True) -> Optional[dict]:
    """AF points on the upright W x H frame, or None when the file doesn't carry them. Never raises."""
    try:
        words, orientation = _read_exifread(path)
        raw = parse_afinfo2(words) if words else None
        if raw:
            raw["source"] = "canon_afinfo2"
        else:
            raw, orientation = _read_exiftool(path)
        if not raw or not raw["points"]:
            return None
        return to_frame(raw, W, H, orientation, y_up)
    except Exception:
        return None


def _overlap(a, b) -> float:
    """Fraction of box a that lies inside box b."""
    ix = max(0, min(a[2], b[2]) - max(a[0], b[0]))
    iy = max(0, min(a[3], b[3]) - max(a[1], b[1]))
    area = max(1, (a[2] - a[0]) * (a[3] - a[1]))
    return ix * iy / area


def _centering(a, b) -> float:
    """1 when box a's center sits on box b's center, falling to 0.75 at b's edge (breaks ties between overlapping people)."""
    ax, ay = (a[0] + a[2]) / 2, (a[1] + a[3]) / 2
    bx, by = (b[0] + b[2]) / 2, (b[1] + b[3]) / 2
    dx = abs(ax - bx) / max(1, (b[2] - b[0]) / 2)
    dy = abs(ay - by) / max(1, (b[3] - b[1]) / 2)
    return 1 - 0.25 * min(1.0, max(dx, dy))


def person_score(af: Optional[dict], person: dict) -> float:
    """How strongly the active AF points land on this person: head hits count double, torso 1.5, body 1,
    each scaled by how much of the point lies inside the region and how central it sits there."""
    if not af or not af.get("active"):
        return 0.0
    by_i = {p["i"]: p["box"] for p in af["points"]}
    s = 0.0
    for i in af["active"]:
        b = by_i.get(i)
        if b is None:
            continue
        best = 0.0
        for key, wgt in (("head", 2.0), ("torso", 1.5), ("box", 1.0)):
            if person.get(key):
                best = max(best, wgt * _overlap(b, person[key]) * _centering(b, person[key]))
        s += best
    return round(s, 3)
