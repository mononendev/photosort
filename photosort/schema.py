"""Output contract for the vision model, shared by all backends."""
from __future__ import annotations
import json

SUBJECTS = ["rider_action", "rider_posed", "group", "crowd_spectators", "gear_board", "venue_scenery", "other", "no_people"]
COMPOSITIONS = ["full_body", "three_quarter", "half_body", "close_up", "environmental", "no_subject"]
PLACEMENTS = ["center", "left_third", "right_third", "top", "bottom", "edge", "none"]

FIELDS = {
    "focus_tier": {"type": "integer", "enum": [0, 1, 2],
                   "description": "0 = nobody in focus; 1 = someone somewhat/mostly in focus; 2 = primary person crisply in focus (head/eyes/helmet edges)"},
    "focus_notes": {"type": "string", "description": "What is and isn't sharp; where focus landed; motion blur vs missed focus. 1-2 sentences."},
    "primary_subject": {"type": "string", "enum": SUBJECTS},
    "people_count": {"type": "integer", "description": "Number of clearly visible people, 0 if none."},
    "composition": {"type": "string", "enum": COMPOSITIONS,
                    "description": "Portion of the primary subject in frame. environmental = subject small in a big scene."},
    "subject_placement": {"type": "string", "enum": PLACEMENTS},
    "action": {"type": "string", "description": "Short phrase for what the primary subject is doing, e.g. 'carving a berm', 'mid-air jump', 'standing and talking', or 'none'."},
    "keywords": {"type": "array", "items": {"type": "string"}, "minItems": 5, "maxItems": 15,
                 "description": "Lowercase nouns/phrases for what is in the photo: objects, setting, gear, tricks, weather, time of day."},
    "adjectives": {"type": "array", "items": {"type": "string"}, "minItems": 3, "maxItems": 8,
                   "description": "Mood, style and light descriptors, e.g. 'dynamic', 'golden-hour', 'gritty'."},
    "description": {"type": "string", "description": "One-sentence caption."},
    "quality_remarks": {"type": "string", "description": "1-3 sentences a photo editor would write: exposure, motion blur, noise, clipped highlights, distracting elements, horizon, crop suggestion."},
    "quality_score": {"type": "integer", "minimum": 1, "maximum": 5, "description": "Overall keeper quality, 1 worst to 5 best."},
    "keeper": {"type": "boolean", "description": "Would a photographer keep this in the delivered set?"},
}


MAX_LENGTHS = {"focus_notes": 220, "action": 60, "description": 140, "quality_remarks": 260}


def json_schema(strict: bool = False, max_lengths: bool = False) -> dict:
    """max_lengths=True adds string caps (used with grammar-constrained local models to stop runaway text)."""
    props = {k: dict(v) for k, v in FIELDS.items()}
    if max_lengths:
        for k, n in MAX_LENGTHS.items():
            props[k]["maxLength"] = n
        props["keywords"]["items"] = {"type": "string", "maxLength": 40}
        props["adjectives"]["items"] = {"type": "string", "maxLength": 30}
    s = {"type": "object", "properties": props, "required": list(FIELDS)}
    if strict:
        s["additionalProperties"] = False
    return s


SYSTEM_PROMPT = """You are assisting an action-sports photographer who shoots onewheel (self-balancing electric board) events with fast lenses near wide open, so depth of field is shallow and focus can vary across a single person's body.

For each photo you get:
- Image 1: the full frame, downscaled.
- Image 2 (when present): a crop at native pixel resolution around the primary person's head and upper body, chosen by a person detector. Judge fine focus from this crop; the downscaled frame cannot show it.
- Detector data: people count, primary subject size and position, and sharpness numbers measured on the original pixels (contrast-normalized; higher is sharper; head/torso/body for the primary person, plus the background). Use them as evidence, not as the answer; the crop is what you can see.

Focus tiers:
- 2: the primary person's head (eyes, face, or helmet edges) is crisply in focus.
- 1: someone is somewhat or mostly in focus: slightly soft, or focus on the torso/board rather than the head, or one of several people is sharp, or slight motion blur with a still-recognizable sharp head.
- 0: nobody in focus: no people, everyone blurry, or focus landed on the background or foreground.
Distinguish missed focus (whole subject soft while something else is crisp) from motion blur (directional smear) in focus_notes.

Composition is the portion of the primary subject in frame. Placement is where the subject sits in the frame.
Keywords are concrete lowercase library tags for what is in the photo. Adjectives describe mood, style and light. Quality remarks are an editor's cull notes about the photo itself: exposure, blur, noise, clipping, distractions, crop.
Write like a photo editor, in short plain sentences about the photo only: focus_notes at most 25 words, description at most 15 words, quality_remarks at most 35 words, 5-10 keywords, 3-5 adjectives. Return only the JSON object."""


def context_text(local: dict) -> str:
    """Per-image detector summary passed alongside the images."""
    if not local:
        return "Detector data unavailable."
    n = local.get("n_people", 0)
    lines = [f"Frame: {local['width']}x{local['height']} {local['orientation']}. People detected: {n}."]
    if (local.get("exif_prior") or {}).get("summary"):
        lines.append(f"Camera: {local['exif_prior']['summary']}")
    if n and local.get("people"):
        p = local["people"][0]
        af = local.get("af") or {}
        if local.get("primary_by") == "af":
            lines.append(f"The camera's AF points ({af.get('mode_name')}) were on this person: they are the intended "
                         f"subject even if someone else is larger or sharper. Grade focus on them.")
        elif af.get("active"):
            lines.append(f"The camera's AF points ({af.get('mode_name')}) were not on any detected person.")
        lines.append(
            f"Primary subject: {p['area_frac']*100:.1f}% of frame, center at ({p['center'][0]:.2f}, {p['center'][1]:.2f}) "
            f"(0,0 = top-left). Head located by {p['head_src']}.")
        lines.append(
            f"Sharpness (higher = sharper): head {p['sharp_head']}, torso {p['sharp_torso']}, body {p['sharp_body']}, "
            f"background {local.get('bg_sharp')}, whole frame {local.get('global_sharp')}.")
        if p.get("sharp_eye") is not None:
            lines.append(
                f"Eye band (both eyes, located by {'face landmarks' if p.get('eye_src') == 'face' else 'pose keypoints'}): "
                f"sharpness {p['sharp_eye']}, fine-detail energy ratio {p.get('hf_eye')}. This is what decides focus.")
        else:
            lines.append("Eyes not located (helmet, visor, turned away, or too small); judge the head.")
        if n > 1:
            others = ", ".join(str(q.get("sharp_head") or q.get("sharp_body")) for q in local["people"][1:4])
            lines.append(f"Other people head sharpness: {others}.")
        lines.append(f"Local focus guess: tier {local['local_tier']} ({local['local_reason']}).")
    else:
        lines.append(f"Whole-frame sharpness {local.get('global_sharp')}. Local focus guess: tier 0 (no people).")
    return "\n".join(lines)


def validate(d: dict) -> dict:
    """Light normalization of a parsed response; raises on missing required fields."""
    missing = [k for k in FIELDS if k not in d]
    if missing:
        raise ValueError(f"missing fields: {missing}")
    d["focus_tier"] = int(d["focus_tier"])
    d["keywords"] = sorted({str(k).strip().lower() for k in d["keywords"] if str(k).strip()})
    d["adjectives"] = sorted({str(k).strip().lower() for k in d["adjectives"] if str(k).strip()})
    if d["primary_subject"] not in SUBJECTS:
        d["primary_subject"] = "other"
    if d["composition"] not in COMPOSITIONS:
        d["composition"] = "no_subject"
    return d


if __name__ == "__main__":
    print(json.dumps(json_schema(True), indent=2))
