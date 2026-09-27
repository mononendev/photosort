"""Why a photo ended up where it did: every rule the pipeline applies to one image, in order, with the values it
read, whether it fired and what it did. Feeds the Trace page.

The walk reads the stored results against the current config and calls the pipeline's own functions (local._grade,
soft_in_front, sharp_other, metric_split, af.person_score, sort.final_record), so a rule can't read differently
here than it does there. The branching of local.local_tier is spelled out step by step; local_tier itself runs too,
and the two answers are returned side by side ("check") so the page can say if they ever part.
"""
from __future__ import annotations
import copy
import math
from pathlib import Path
from typing import Any, Optional

from . import af as A
from . import exif as X
from . import local as L
from . import sort as S
from .db import jcol, vlm_stale
from .images import RAW_EXT


def _r(v, n: int = 4):
    return None if v is None else round(float(v), n)


def _node(q: str, result: Optional[bool], *, rule: Optional[str] = None, inputs: Optional[list] = None,
          effect: Optional[str] = None, note: Optional[str] = None, reached: bool = True, people: Optional[list] = None,
          decided: bool = False) -> dict:
    """One rule the pipeline evaluates. result: True it holds (fires), False it doesn't, None it's off or has nothing
    to read. Every rule is evaluated, even where it can't matter: reached is False when an earlier rule already
    decided or its branch wasn't taken, and effect is then what it would have done. decided marks the one rule
    that settled the stage."""
    return {"q": q, "result": result, "rule": rule, "inputs": inputs or [], "effect": effect, "note": note,
            "reached": reached, "people": people, "decided": decided}


def _kv(k: str, v: Any, note: Optional[str] = None) -> dict:
    return {"k": k, "v": v, **({"note": note} if note else {})}


class _Chain:
    """Rules asked in order until one decides. Later rules are still evaluated and listed, marked not reached."""

    def __init__(self):
        self.nodes: list[dict] = []
        self.decided = False
        self.value: Any = None

    def ask(self, q: str, result: Optional[bool], decides: bool = False, value: Any = None, gate: Optional[str] = None,
            **kw) -> dict:
        """gate: why this rule's branch wasn't taken (it's evaluated anyway). value: what deciding sets."""
        reached = not self.decided and gate is None
        n = _node(q, result, reached=reached, **kw)
        if not reached:
            n["note"] = ("not reached: decided above" if self.decided else gate) + (f" · {n['note']}" if n["note"] else "")
        elif result and decides:
            self.decided, self.value, n["decided"] = True, value, True
        self.nodes.append(n)
        return n


def _stage(key: str, title: str, state: str, summary: str, *, facts=None, nodes=None, table=None, outcome=None) -> dict:
    """state: done (ran and decided), skipped (a rule left it out), pending (hasn't run), error, off."""
    return {"key": key, "title": title, "state": state, "summary": summary, "facts": facts or [],
            "nodes": nodes or [], "table": table, "outcome": outcome}


def _pid(people: list[dict], p: dict) -> int:
    """A person's number as the detail view shows it (1-based, in stored order)."""
    return next((i + 1 for i, q in enumerate(people) if q.get("box") == p.get("box")), 0)


# ---- stages -------------------------------------------------------------------------------------------------

def _scan(row, local, rel: str) -> dict:
    ext = Path(row["path"]).suffix.lower()
    lr = jcol(row, "lr_json", {}) or {}
    facts = [_kv("file", rel), _kv("size", f"{(row['size'] or 0) / 1e6:.1f} MB" if row["size"] else None),
             _kv("type", "RAW" if ext in RAW_EXT else ext.lstrip(".").upper(),
                 "a RAW is only scanned when no JPEG/HEIC shares its name (skip_raw_dupes)" if ext in RAW_EXT else None)]
    if lr.get("rating") is not None or lr.get("label"):
        facts.append(_kv("sidecar", f"{lr.get('rating', '–')}★ {lr.get('label') or ''}".strip(),
                         "read from an existing .xmp next to the file; informational only"))
    state = "error" if row["error"] and not local else "done"
    return _stage("scan", "Scan", state, "registered" + (f" · {row['error']}" if state == "error" else ""), facts=facts)


def _exposure(local, cfg) -> dict:
    ex = cfg.get("exposure") or {}
    if local is None:
        return _stage("exposure", "Exposure lift", "pending", "runs with the local stage")
    e = local.get("exposure")
    ch = _Chain()
    ch.ask("Lift dark frames?", bool(ex.get("recover")), rule=f"exposure.recover = {ex.get('recover')}",
           note=None if ex.get("recover") else "off: frames are measured and sent as shot")
    if e:
        kind = e.get("source", "jpeg")
        dark, cap_ev = ex.get(f"{kind}_dark_key"), ex.get(f"{kind}_max_ev")
        want = math.log2(ex["target_key"] / e["key"]) if e.get("key") and ex.get("target_key") else None
        hl = math.log2(ex["highlight_cap"] / max(e.get("p99") or 1e-6, 1e-6)) if ex.get("highlight_cap") else None
        ch.ask(f"Scene key under the {kind.upper()} dark cut?", True, rule=f"exposure.{kind}_dark_key = {dark}",
               inputs=[_kv("key", e.get("key"), "log-average luminance, linear light")])
        ch.ask("Lift at least min_ev?", True, decides=True, rule=f"exposure.min_ev = {ex.get('min_ev')}",
               inputs=[_kv("to target", _r(want, 2), f"log2(target_key {ex.get('target_key')} / key)"),
                       _kv("cap", cap_ev, f"exposure.{kind}_max_ev"),
                       _kv("highlight room", _r(hl, 2), f"log2(highlight_cap {ex.get('highlight_cap')} / p99 {e.get('p99')})"),
                       _kv("lift", e["ev"], "the smallest of the three")],
               effect=f"+{e['ev']} EV, from the {'embedded camera JPEG' if kind == 'raw' else 'JPEG'}")
        return _stage("exposure", "Exposure lift", "done", f"+{e['ev']} EV", nodes=ch.nodes,
                      outcome={"label": f"lifted +{e['ev']} EV"})
    ch.ask("Dark enough to lift, by at least min_ev?", False,
           rule=f"raw_dark_key {ex.get('raw_dark_key')} · jpeg_dark_key {ex.get('jpeg_dark_key')} · min_ev {ex.get('min_ev')}",
           note="no lift recorded at the last local pass (the key of an unlifted frame isn't stored)")
    return _stage("exposure", "Exposure lift", "skipped", "not lifted", nodes=ch.nodes, outcome={"label": "as shot"})


def _noise_now(local: dict, cfg: dict) -> dict:
    """exif.noise_prior on the stored measurement under the current config, as a rescore computes it."""
    return X.noise_prior(local.get("exif") or {}, (local.get("exposure") or {}).get("ev"),
                         (local.get("noise") or {}).get("sigma"), cfg.get("noise"))


def _noise(local, cfg) -> dict:
    """exif.noise_prior: measured noise decides, effective ISO stands in for rows analyzed before it was measured.
    High risk makes a tier 3 clear the cuts by noise.margin (the noisy rule in the local tier)."""
    if local is None:
        return _stage("noise", "Noise", "pending", "measured with the local stage")
    nc = cfg.get("noise") or {}
    nz = _noise_now(local, cfg)
    if nz["by"] is None:
        return _stage("noise", "Noise", "skipped", "no measurement and no ISO", outcome={"label": "unknown"})
    measured = nz["by"] == "measured"
    iso_in = [_kv("ISO", nz["iso"]), _kv("lift", f"+{nz['ev']} EV" if nz["ev"] else None),
              _kv("effective ISO", nz["eff_iso"], "ISO × 2^lift")]
    ch = _Chain()
    ch.ask("Measured noise high?" if measured else "Effective ISO high?", nz["risk"] == "high", decides=True, value="high",
           rule=f"noise.high_sigma = {nc.get('high_sigma')} levels" if measured else f"noise.high_iso = {nc.get('high_iso')}",
           inputs=[_kv("noise sigma", nz["sigma"], "8-bit levels, flattest half of the frame, after any lift")] if measured else iso_in,
           effect="a tier 3 must clear the cuts by noise.margin")
    ch.ask("Measured noise medium?" if measured else "Effective ISO medium?", nz["risk"] == "medium", decides=True, value="medium",
           rule=f"noise.medium_sigma = {nc.get('medium_sigma')} levels" if measured else f"noise.noisy_iso = {nc.get('noisy_iso')}",
           effect="noted for the model; no tier change")
    facts = iso_in if measured else [*iso_in, _kv("noise sigma", nz["sigma"], "8-bit levels; set noise.medium_sigma and "
                                                  "high_sigma to judge on it" if nz["sigma"] is not None else
                                                  "not measured: analyzed before noise was; re-run the local stage")]
    return _stage("noise", "Noise", "done", f"{nz['risk']} ({'measured' if measured else 'from ISO'})", facts=facts,
                  nodes=ch.nodes, outcome={"label": f"noise {nz['risk'] or '–'}"})


def _exif(local, cfg) -> dict:
    """exif.prior: motion-blur and depth-of-field risk from the exposure settings. Only high motion risk can move a
    tier (the slow-shutter rule in the local tier); the rest is context for the notes and the vision prompt."""
    if local is None:
        return _stage("exif", "Camera settings", "pending", "read with the local stage")
    ex = local.get("exif") or {}
    if not ex:
        return _stage("exif", "Camera settings", "skipped", "no EXIF", outcome={"label": "no EXIF"})
    ec = cfg.get("exif") or {}
    pr = X.prior(ex, ec)
    s, f = ex.get("shutter_s"), ex.get("f_number")
    act = ec.get("action_shutter", 1 / 500)
    ss = pr.get("shake_stops")
    ch = _Chain()
    ch.ask("Motion risk high?", pr["motion_risk"] == "high" if s else None, decides=True, value="high",
           rule="a stop or more past 1/focal length, or 1/60 s and slower",
           inputs=[_kv("shutter", X.fmt_shutter(s) if s else None), _kv("vs 1/focal", f"{ss:+} stops" if ss is not None else None,
                                                                      f"crop_factor {ec.get('crop_factor', 1.0)} when EXIF lacks the 35mm focal")],
           effect="a tier 3 must clear the cuts by exif.shake_margin")
    ch.ask("Motion risk medium?", pr["motion_risk"] == "medium" if s else None, decides=True, value="medium",
           rule=f"within a stop of 1/focal length, or slower than exif.action_shutter ({X.fmt_shutter(act)})",
           effect="noted for the model; no tier change")
    dof = _node("Very shallow depth of field?", pr["dof_risk"] == "high" if f else None,
                rule=f"f ≤ {ec.get('wide_open_f', 2.0)} (exif.wide_open_f) or entrance pupil ≥ 40 mm",
                inputs=[_kv("aperture", f"f/{f:g}" if f else None), _kv("pupil", pr.get("pupil_mm"), "mm, focal ÷ f-number")],
                effect="noted: judge the eyes; no tier change")
    ch.nodes.append(dof)
    return _stage("exif", "Camera settings", "done", pr.get("summary") or "read",
                  facts=[_kv("camera", pr.get("summary"))], nodes=ch.nodes,
                  outcome={"label": f"motion {pr['motion_risk'] or '–'} · DOF {pr['dof_risk'] or '–'}"})


def _detect(local, cfg) -> dict:
    if local is None:
        return _stage("detect", "People", "pending", "local stage hasn't run")
    people = local.get("people") or []
    facts = [_kv("found", local.get("n_people", 0), f"{len(people)} stored" if local.get("n_people", 0) > len(people) else None),
             _kv("pose model", cfg.get("detect_model")),
             _kv("confidence ≥", cfg.get("detect_conf"), "detect_conf"),
             _kv("smallest person", cfg.get("min_person_frac"), "min_person_frac × frame area"),
             _kv("duplicates", f"IoU ≥ {cfg.get('dedup_iou')}, or ≥ {cfg.get('dedup_head_iou')} with the same head",
                 "dedup_iou / dedup_head_iou / dedup_head_tol")]
    rows = [{"n": i + 1, "conf": _r(p.get("conf"), 3), "area": _r(p.get("area_frac"), 4), "center": _r(p.get("center_dist"), 3),
             "priority": p.get("priority"), "head_src": p.get("head_src")} for i, p in enumerate(people)]
    n = local.get("n_people", 0)
    return _stage("detect", "People", "done" if n else "skipped", f"{n} {'person' if n == 1 else 'people'}", facts=facts,
                  table={"kind": "people", "rows": rows}, outcome={"label": f"{n} found"})


def _primary(local, cfg, people: list[dict], picked: list[dict], by: str) -> dict:
    if local is None:
        return _stage("primary", "Primary subject", "pending", "local stage hasn't run")
    if not people:
        return _stage("primary", "Primary subject", "skipped", "nobody to pick", outcome={"label": "none"})
    acfg = cfg.get("af") or {}
    af = local.get("af")
    scores = [{"n": _pid(people, p), "af_score": p.get("af_score"), "priority": p.get("priority")} for p in picked]
    best = max(picked, key=lambda p: (p.get("af_score") or 0, p.get("priority") or 0)) if af else None
    ch = _Chain()
    ch.ask("AF points in the file?", af is not None, inputs=[_kv("read", local.get("af_note"))])
    if af is not None:
        ch.ask("Let AF points pick?", bool(acfg.get("use", True)), rule=f"af.use = {acfg.get('use', True)}")
        ch.ask("Any active points?", bool(af.get("active")),
               inputs=[_kv("mode", af.get("mode_name") + ("" if af.get("user_placed") else " (camera-chosen)")),
                       _kv("active", f"{len(af.get('active') or [])} of {af.get('n_points')}",
                           "points that reported focus, else the selected ones")])
        gate = None if acfg.get("use", True) and af.get("active") else "AF can't pick: off, or no active points"
        ok = (best.get("af_score") or 0) >= acfg.get("min_score", 0.5)
        ch.ask("Best AF hit score reaches min_score?", ok, decides=True, gate=gate,
               rule=f"af.min_score = {acfg.get('min_score', 0.5)} · af.near = {acfg.get('near', 0)}",
               inputs=[_kv(f"person #{_pid(people, best)}", best.get("af_score"),
                           "head hit 2, torso 1.5, body 1 per point; up to half that just beside them")],
               effect=f"person #{_pid(people, best)} is the primary (AF)")
    prom = max(picked, key=lambda p: p.get("priority") or 0)
    ch.ask("Most prominent person", True, decides=True,
           inputs=[_kv(f"person #{_pid(people, prom)}", prom.get("priority"),
                       "area × (1 − 0.5 × distance from center) × (0.5 + 0.5 × confidence)")],
           effect=f"person #{_pid(people, prom)} is the primary (prominence)")
    top = picked[0]
    stored_by = local.get("primary_by")
    note = None
    if people[0].get("box") != top.get("box") or (stored_by and stored_by != by):
        note = f"The stored results still have person #1 ({stored_by}) as primary; saving the config re-scores and picks #{_pid(people, top)}."
    return _stage("primary", "Primary subject", "done", f"person #{_pid(people, top)} by {'AF' if by == 'af' else 'prominence'}",
                  nodes=ch.nodes, table={"kind": "scores", "rows": scores},
                  facts=[_kv("stale", note)] if note else [],
                  outcome={"label": f"#{_pid(people, top)} ({'AF' if by == 'af' else 'prominence'})", "person": _pid(people, top)})


def _eyes(local, cfg, people, primary) -> dict:
    if local is None:
        return _stage("eyes", "Where it's judged", "pending", "local stage hasn't run")
    if primary is None:
        return _stage("eyes", "Where it's judged", "skipped", "no primary subject")
    thr = cfg["focus"]
    face, src = primary.get("face"), primary.get("eye_src")
    on_eyes = thr.get("use_eyes", True) and primary.get("sharp_eye") is not None and "eye_tier3_min" in thr
    ch = _Chain()
    ch.ask("Face model found this head's face?", face is not None, rule=f"face_conf = {cfg.get('face_conf')}",
           inputs=[_kv("score", face.get("score"))] if face else [],
           note=None if face else "falls back to the pose model's eye keypoints (confidence ≥ 0.5)")
    ch.ask("Eye band located and measurable?", primary.get("sharp_eye") is not None,
           inputs=[_kv("from", {"face": "face landmarks", "pose": "pose keypoints"}.get(src or "", "—"))],
           note=None if primary.get("sharp_eye") is not None else
           ("only the top eye_max_people people get eye bands" if _pid(people, primary) > cfg.get("eye_max_people", 4)
            else "helmet, visor, turned away, or inter-eye distance under 8 px / band under 24 px"))
    ch.ask("Judge on the eye band?", on_eyes, decides=True, rule=f"focus.use_eyes = {thr.get('use_eyes', True)}",
           effect="eye band Laplacian" + (" + FFT ratio" if thr.get("use_hf", True) and primary.get("hf_eye") is not None else ""))
    if not on_eyes:
        ch.nodes.append(_node("Head box measurable?", primary.get("sharp_head") is not None,
                              inputs=[_kv("head from", primary.get("head_src"), "keypoints, or guessed from the box top")],
                              effect="head box Laplacian" if primary.get("sharp_head") is not None else
                              ("body box Laplacian" if primary.get("sharp_body") is not None else "nothing measurable")))
    else:
        ew = L.eyewear(primary, thr)
        ch.nodes.append(_node("Eye band looks like eyewear?", ew if thr.get("eyewear_ratio") else None,
                              rule=f"focus.eyewear_ratio = {thr.get('eyewear_ratio')}",
                              inputs=[_kv("eye / head", _r(primary["sharp_eye"] / primary["sharp_head"], 2) if primary.get("sharp_head") else None)],
                              effect="the head box must clear each tier too" if ew else None))
    basis = ("eye band" if on_eyes else "head box" if primary.get("sharp_head") is not None else "body box")
    return _stage("eyes", "Where it's judged", "done", basis, nodes=ch.nodes, outcome={"label": basis})


def _grade_table(p: dict, thr: dict) -> tuple[Optional[int], dict]:
    """The grade (local._grade) and the per-tier pass/fail matrix behind it."""
    on_eyes = thr.get("use_eyes", True) and p.get("sharp_eye") is not None and "eye_tier3_min" in thr
    rows = []
    if on_eyes:
        rows.append(("eye band Laplacian", p["sharp_eye"], "eye_"))
        if thr.get("use_hf", True) and p.get("hf_eye") is not None and "hf_tier3_min" in thr:
            rows.append(("eye band FFT ratio", p["hf_eye"], "hf_"))
        if L.eyewear(p, thr):
            rows.append(("head box Laplacian (eyewear)", p.get("sharp_head"), ""))
    else:
        s = p.get("sharp_head") or p.get("sharp_body")
        rows.append(("head box Laplacian" if p.get("sharp_head") else "body box Laplacian", s, ""))
    out = [{"label": lab, "value": _r(v), "cuts": [thr.get(f"{pre}tier{n}_min") for n in (3, 2, 1)],
            "ok": [v is not None and v >= thr[f"{pre}tier{n}_min"] for n in (3, 2, 1)]} for lab, v, pre in rows]
    return L._grade(p, thr), {"kind": "grade", "tiers": [3, 2, 1], "rows": out}


def _grade(local, cfg, primary) -> dict:
    if local is None:
        return _stage("grade", "Grade", "pending", "local stage hasn't run")
    if primary is None:
        return _stage("grade", "Grade", "skipped", "no primary subject")
    g, table = _grade_table(primary, cfg["focus"])
    return _stage("grade", "Grade", "done", "nothing measurable" if g is None else f"grade {g}", table=table,
                  outcome={"label": "—" if g is None else f"grade {g}", "tier": g},
                  facts=[_kv("rule", "the highest tier every row clears")])


def _local_tier(local, cfg, people, primary, others) -> tuple[dict, dict]:
    """local.local_tier, one rule at a time and in its order. Every rule is evaluated for this photo; the ones its
    grade or an earlier rule made moot are marked not reached, with what they would have done."""
    if local is None:
        return _stage("local", "Local tier", "pending", "local stage hasn't run"), {}
    thr = cfg["focus"]
    prior = local.get("exif_prior") or {}
    margin = (cfg.get("exif") or {}).get("shake_margin", 1.5)
    noise = _noise_now(local, cfg)
    size =(local["width"], local["height"]) if local.get("width") else None
    g = L._grade(primary, thr) if primary is not None else None
    on_eyes = primary is not None and thr.get("use_eyes", True) and primary.get("sharp_eye") is not None and "eye_tier3_min" in thr
    ch = _Chain()
    tag = lambda t, r: f"tier {t} · {r}"

    ch.ask("Nobody found?", primary is None, decides=True, value=(0, "no_people"),
           effect=tag(0, "no_people"), inputs=[_kv("people", local.get("n_people", 0))])

    # secondary_person_sharp as a floor: someone else confidently detected who grades floor_grade or better
    ft, fc, fg = thr.get("floor_tier"), thr.get("floor_conf", 0.5), thr.get("floor_grade", 3)
    rows = []
    for o in others:
        og = L._grade(o, thr)
        tests = {f"conf ≥ {fc}": (o.get("conf") or 0) >= fc, f"grade ≥ {fg}": (og or 0) >= fg}
        rows.append({"n": _pid(people, o), "conf": _r(o.get("conf"), 3), "grade": og, "tests": tests, "ok": all(tests.values())})
    anyone = L.sharp_other(others, thr) if ft is not None else False
    below = g is None or g < (ft or 0)
    ch.ask(f"Primary below tier {ft}, and someone else sharp?" if ft is not None else "Someone else sharp raises the floor?",
           (below and anyone) if ft is not None else None, decides=True, value=(ft, "secondary_person_sharp"),
           rule=f"focus.floor_tier = {ft} · floor_grade = {fg} · floor_conf = {fc}",
           inputs=[_kv("primary grade", g), _kv("below the floor", below), _kv("someone else qualifies", any(r["ok"] for r in rows))],
           people=rows, effect=tag(ft, "secondary_person_sharp") if ft is not None else None,
           note="off (floor_tier = null)" if ft is None else None)
    ch.ask("Primary unmeasurable?", primary is not None and g is None, decides=True, value=(0, "subject_too_small"),
           effect=tag(0, "subject_too_small"), note="every region under 40 px" if primary is not None and g is None else None)

    # Grade 3 and the rules that can take it down to 2, in local_tier's order
    not3 = None if g == 3 else f"only checked on a grade-3 primary (this one grades {g})"
    ch.ask("Grade 3?", g == 3, inputs=[_kv("grade", g)])
    gm = L._grade(primary, thr, margin) if primary is not None else None
    risky = prior.get("motion_risk") == "high"
    ch.ask("Slow shutter, and not sharp by the margin?", risky and gm is not None and gm < 3, decides=True, gate=not3,
           value=(2, "borderline_sharp_slow_shutter"), effect=tag(2, "borderline_sharp_slow_shutter"),
           rule=f"exif.shake_margin = {margin}",
           inputs=[_kv("motion risk", prior.get("motion_risk"), prior.get("summary")),
                   _kv(f"grade at {margin}× the cuts", gm)])
    nmargin = (cfg.get("noise") or {}).get("margin", 1.5)
    gn = L._grade(primary, thr, nmargin) if primary is not None else None
    noisy = noise.get("risk") == "high"
    ch.ask("High noise, and not sharp by the margin?", noisy and gn is not None and gn < 3, decides=True, gate=not3,
           value=(2, "borderline_sharp_noisy"), effect=tag(2, "borderline_sharp_noisy"),
           rule=f"noise.margin = {nmargin}",
           inputs=[_kv("noise risk", noise.get("risk"), f"by {noise['by']}" if noise.get("by") else None),
                   _kv(f"grade at {nmargin}× the cuts", gn)])
    pl =(primary or {}).get("plane") or {}
    use_plane = thr.get("use_plane", True)
    cut, body_cut = thr.get("plane_max_extra"), thr.get("plane_body_max_extra")
    no_plane = "no plane measurement (head under 40 px, or analyzed before the check existed)" if primary is not None and not pl else None
    ch.ask("Surroundings sharper than the head?", (pl.get("head_vs_near") or 0) >= cut if use_plane and cut and pl else None,
           decides=True, gate=not3, value=(2, "sharper_around_subject"), effect=tag(2, "sharper_around_subject"),
           rule=f"focus.use_plane = {use_plane} · plane_max_extra = {cut} px",
           inputs=[_kv("head blur", pl.get("head"), "edge width, px"),
                   _kv("surroundings blur", pl.get("near"), "too few edges (bokeh)" if pl and pl.get("near") is None else None),
                   _kv("head extra", pl.get("head_vs_near"), "px more than the surroundings, in quadrature")],
           note=no_plane or (None if use_plane and cut else "off"))
    ch.ask("Torso sharper than the head?", (pl.get("head_vs_torso") or 0) >= body_cut if use_plane and body_cut and pl else None,
           decides=True, gate=not3, value=(2, "sharper_body_than_head"), effect=tag(2, "sharper_body_than_head"),
           rule=f"focus.plane_body_max_extra = {body_cut} px",
           inputs=[_kv("torso blur", pl.get("torso")), _kv("head extra", pl.get("head_vs_torso"), "px more than the torso")],
           note=no_plane or (None if body_cut else "off by default: clothing print reads sharper than a face"))
    use_front = thr.get("use_front", True)
    front_rows = _front_rows(primary, others, people, thr, size) if primary is not None and size else []
    hit = L.soft_in_front(primary, others, thr, *size) if primary is not None and size else None
    ch.ask("A soft person stands just in front?", (hit is not None) if use_front else None,
           decides=True, gate=not3, value=(2, "soft_person_in_front"), effect=tag(2, "soft_person_in_front"),
           rule=(f"focus.use_front = {use_front} · front_min_height {thr.get('front_min_height')} · front_min_drop "
                 f"{thr.get('front_min_drop')} · front_max_gap {thr.get('front_max_gap')} · front_max_grade "
                 f"{thr.get('front_max_grade', 1)} · front_edge {thr.get('front_edge', 0.01)}"),
           inputs=[_kv("match", f"person #{_pid(people, others[hit])}" if hit is not None else None)],
           people=front_rows, note=None if use_front else "off")
    r3 = "primary_eyes_sharp" if on_eyes else "primary_head_sharp"
    ch.ask("Keeps tier 3", g == 3, decides=True, gate=not3, value=(3, r3), effect=tag(3, r3))

    # Below 3: the grade is the tier
    for lvl, reason in ((2, "primary_eyes_slightly_soft" if on_eyes else "primary_slightly_soft"),
                        (1, "primary_eyes_soft" if on_eyes else "primary_soft")):
        ch.ask(f"Grade {lvl}?", g == lvl, decides=True, value=(lvl, reason), effect=tag(lvl, reason))
    bystander = any(L._grade(o, thr) == 3 for o in others)
    zr = "secondary_person_sharp" if bystander else "nothing_sharp"
    ch.ask("Grade 0", g == 0, decides=True, value=(0, zr), effect=tag(0, zr),
           inputs=[_kv("someone else grades 3", bystander)],
           note="the tier grades the primary, so a sharp bystander (with the floor off) keeps it a miss; the reason says so" if bystander else None)

    traced = list(ch.value) if ch.value else [None, None]
    engine = list(L.local_tier(primary, others, thr, prior, size=size, noise=noise, **L.margins(cfg)))
    stored = [local.get("local_tier"), local.get("local_reason")]
    facts = []
    if traced != engine:
        facts.append(_kv("trace", f"this walk says {tag(*traced)}, local_tier says {tag(*engine)}",
                         "the trace has fallen behind local.local_tier; the tier used everywhere is local_tier's"))
    if engine != stored:
        facts.append(_kv("stale", f"stored {tag(*stored)}; the current config gives {tag(*engine)}",
                         "saving the config (or `photosort rescore`) updates the stored tier"))
    return (_stage("local", "Local tier", "done", tag(*engine), nodes=ch.nodes, facts=facts,
                   outcome={"label": f"local {engine[0]}", "tier": engine[0], "reason": engine[1]}),
            {"traced": traced, "engine": engine, "stored": stored})


def _front_rows(primary, others, people, thr, size) -> list[dict]:
    """local.soft_in_front's tests on each other person, with the numbers, so the page shows which one failed."""
    W, H = size
    box, e = primary.get("box"), thr.get("front_edge", 0.01)
    if not box:
        return []
    h = box[3] - box[1]
    hmin, drop, gap = (thr.get(k) or 0 for k in ("front_min_height", "front_min_drop", "front_max_gap"))
    mg = thr.get("front_max_grade", 1)
    cut = thr.get(f"tier{mg + 1}_min")
    out = []
    for o in others:
        b, s = o.get("box"), o.get("sharp_head")
        if not b or not h:
            continue
        hr, dr = (b[3] - b[1]) / h, (b[3] - box[3]) / h
        gp = max(b[0] - box[2], box[0] - b[2]) / h
        edge = b[0] <= e * W or b[2] >= (1 - e) * W or b[3] >= (1 - e) * H
        tests = {f"height {hr:.2f}× ≥ {hmin}×": hr >= hmin,
                 f"feet {dr:+.2f} ≥ {drop} lower": dr >= drop,
                 f"gap {max(gp, 0):.2f} ≤ {gap} to the side": gp <= gap,
                 f"clear of the frame edge ({e})": not edge,
                 f"head {'–' if s is None else f'{s:.4f}'} < {cut} (tier {mg} or worse)": s is not None and cut is not None and s < cut}
        out.append({"n": _pid(people, o), "tests": tests, "ok": all(tests.values())})
    return out


def _split(local, cfg, primary) -> dict:
    if local is None:
        return _stage("split", "Metrics agree?", "pending", "local stage hasn't run")
    thr = cfg["focus"]
    steps = thr.get("split_steps")
    sp = L.metric_split(primary, thr)
    grades = {}
    if primary is not None:
        for name, v, pre in (("eye", primary.get("sharp_eye"), "eye_"), ("fft", primary.get("hf_eye"), "hf_"), ("head", primary.get("sharp_head"), "")):
            if v is not None and f"{pre}tier3_min" in thr:
                grades[name] = next((lvl for lvl in (3, 2, 1) if v >= thr[f"{pre}tier{lvl}_min"]), 0)
    n = _node("One metric far from the others?", (sp is not None) if steps else None, rule=f"focus.split_steps = {steps}",
              inputs=[_kv(k, g) for k, g in grades.items()],
              effect=f"{sp['odd']} is {sp['gap']} tiers off (a filter in Review; the tier is left alone)" if sp else None,
              note="off" if not steps else ("fewer than two metrics measured" if len(grades) < 2 else None))
    return _stage("split", "Metrics agree?", "done" if steps else "off", "split" if sp else "agree" if steps else "off",
                  nodes=[n], outcome={"label": "split" if sp else "agree"})


def _vlm(row, local, vlm, cfg) -> dict:
    stale = vlm_stale(local, vlm)
    usage = jcol(row, "vlm_usage", {}) or {}
    if vlm is None:
        if row["vlm_skip"]:
            n = _node("Sent to the vision model?", False, inputs=[_kv("skipped", row["vlm_skip"])],
                      note="the job had “skip nobody-in-focus” on; re-tag with it off to have the model look")
            return _stage("vlm", "Vision model", "skipped", row["vlm_skip"], nodes=[n], outcome={"label": "skipped"})
        return _stage("vlm", "Vision model", "error" if row["error"] and local else "pending",
                      row["error"] or ("not tagged yet" if local else "waits for the local stage"), outcome={"label": "—"})
    facts = [_kv("focus notes", vlm.get("focus_notes")),
             _kv("subject", f"{vlm.get('primary_subject')} · {vlm.get('composition')} · {vlm.get('subject_placement')}"),
             _kv("score", f"{vlm.get('quality_score')} · {'keeper' if vlm.get('keeper') else 'cull'}"),
             _kv("model", usage.get("model") or cfg.get("model") or cfg.get("backend"))]
    ev_seen = vlm.get("seen_ev") or 0
    ev_now = ((local or {}).get("exposure") or {}).get("ev") or 0
    n = _node("Verdict made on the frame the local stage has now?", not stale,
              inputs=[_kv("model saw", f"+{ev_seen} EV"), _kv("local has", f"+{ev_now} EV")],
              effect=None if not stale else "stale: counts as not run until the model re-tags it",
              note="the model judged a frame with a different exposure lift" if stale else None)
    return _stage("vlm", "Vision model", "done", f"tier {vlm.get('focus_tier')}" + (" · stale" if stale else ""), facts=facts,
                  nodes=[n], outcome={"label": f"model {vlm.get('focus_tier')}" + (" (stale)" if stale else ""), "tier": vlm.get("focus_tier")})


def _final(rec, local, vlm, ov, cfg) -> dict:
    src = cfg.get("focus_source", "vlm")
    lt = local["local_tier"] if local else None
    vt = None if vlm is None or vlm_stale(local, vlm) else vlm.get("focus_tier")
    ch = _Chain()
    ch.ask("You rated it?", ov.get("focus_tier") is not None, decides=True,
           inputs=[_kv("your rating", ov.get("rating"))] if ov.get("rating") is not None else [],
           effect=f"tier {ov.get('focus_tier')}: your call beats everything" if ov.get("focus_tier") is not None else None)
    if src == "local":
        ch.ask("focus_source = local", True, decides=True, rule="focus_source = local", effect=f"tier {lt} (local)")
    elif src == "strict":
        both = lt is not None and vt is not None
        ch.ask("Both tiers available?", both, decides=both, rule="focus_source = strict",
               inputs=[_kv("local", lt), _kv("model", vt)], effect=f"tier {min(lt, vt)}: the lower of the two" if both else None)
        ch.ask("Fall back to whichever ran", True, decides=True, effect=f"tier {lt if vt is None else vt}")
    else:
        ch.ask("Usable model tier?", vt is not None, decides=vt is not None, rule=f"focus_source = {src}",
               inputs=[_kv("model", vlm.get("focus_tier") if vlm else None, "stale" if vlm and vt is None else None)],
               effect=f"tier {vt} (model)" if vt is not None else None)
        ch.ask("Fall back to the local tier", True, decides=True, effect=f"tier {lt} (local)")
    t = rec["focus_tier"]
    return _stage("final", "Final tier", "done" if t is not None else "pending", "—" if t is None else f"tier {t}",
                  nodes=ch.nodes, outcome={"label": "—" if t is None else f"final {t}", "tier": t})


def _review(rec, ov) -> dict:
    lt, vt = rec["focus_tier_local"], rec["focus_tier_vlm"]
    nodes = [
        _node("Local and model disagree?", rec["disagree"] if vt is not None else None,
              inputs=[_kv("local", lt), _kv("model", vt)], effect="needs review" if rec["disagree"] else None,
              note=None if vt is not None else "no model verdict (or a stale one) to compare"),
        _node("You've rated it?", bool(rec["reviewed"]), effect="off the Review queue (it shows unrated photos)" if rec["reviewed"] else None),
    ]
    label = ("flagged" if rec["review"] else "not flagged") + (" · rated" if rec["reviewed"] else "")
    return _stage("review", "Review", "done", label, nodes=nodes, outcome={"label": label})


def _export(rec, row) -> dict:
    name = Path(row["path"]).name
    if rec["focus_tier"] is None:
        return _stage("export", "Export", "pending", "not exported until it has a tier", outcome={"label": "—"})
    tier_dir = S.TIER_NAMES[rec["focus_tier"]]
    unknown = rec["subject"] == "unknown" and rec["composition"] == "unknown"
    paths = [f"{tier_dir}/{name}" if unknown else f"{tier_dir}/{rec['subject']}/{rec['composition']}/{name}"]
    if rec["review"]:
        why = f"local{rec['focus_tier_local']}_vlm{rec['focus_tier_vlm']}"
        paths.append(f"review/{why}/{name}")
    if rec.get("banger"):
        paths.append(f"bangers/{name}")
    facts = [_kv("XMP label", S.RATING_LABELS.get(rec["rating"]) if rec.get("rating") is not None else None,
                 "your rating's color; none until you rate it"),
             _kv("XMP stars", rec.get("quality_score"), "quality score (yours, else the model's)"),
             _kv("keywords", f"focus-{tier_dir} · subject-{rec['subject']} · comp-{rec['composition']}"
                 + (" · photosort-review" if rec["review"] else "") + (" · photosort-banger" if rec.get("banger") else ""))]
    return _stage("export", "Export", "done", paths[0], facts=facts, table={"kind": "paths", "rows": paths},
                  outcome={"label": tier_dir})


# ---- entry point --------------------------------------------------------------------------------------------

def trace(row, cfg: dict, rel: str) -> dict:
    local, vlm = jcol(row, "local_json"), jcol(row, "vlm_json")
    ov = jcol(row, "override_json", {}) or {}
    people = (local or {}).get("people") or []
    # Re-pick the primary under the current config, as a re-score would; the stored order is kept for numbering.
    picked = copy.deepcopy(people)
    by = L.pick_primary(picked, (local or {}).get("af"), cfg) if people else "priority"
    primary = picked[0] if picked else None
    others = picked[1:]
    rec = S.final_record(row, cfg.get("focus_source", "vlm"))
    local_stage, check = _local_tier(local, cfg, people, primary, others)
    stages = [
        _scan(row, local, rel),
        _exposure(local, cfg),
        _noise(local, cfg),
        _exif(local, cfg),
        _detect(local, cfg),
        _primary(local, cfg, people, picked, by),
        _eyes(local, cfg, people, primary),
        _grade(local, cfg, primary),
        local_stage,
        _split(local, cfg, primary),
        _vlm(row, local, vlm, cfg),
        _final(rec, local, vlm, ov, cfg),
        _review(rec, ov),
        _export(rec, row),
    ]
    return {"id": row["id"], "rel": rel, "focus_source": cfg.get("focus_source", "vlm"), "stages": stages, "check": check,
            "primary": _pid(people, primary) if primary else None,
            "final": {"tier": rec["focus_tier"], "local": rec["focus_tier_local"], "vlm": rec["focus_tier_vlm"],
                      "review": rec["review"], "rating": rec["rating"]}}
