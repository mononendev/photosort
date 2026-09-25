from __future__ import annotations
import json
from pathlib import Path

DEFAULTS: dict = {
    # What gets sent to the cloud model
    "frame_long_edge": 1568,     # downscaled full frame (all providers cap image tokens near here)
    "frame_quality": 82,
    "crop_size": 768,            # native-resolution crop around the primary subject (long edge)
    "crop_quality": 88,
    "crop_pad": 0.15,            # padding around the detector box, fraction of box size
    # Local stage
    "detect_long_edge": 1280,
    "detect_conf": 0.25,
    "detect_model": "yolo11n-pose.pt",
    "min_person_frac": 0.0015,   # ignore boxes smaller than this fraction of the frame
    "workers": 4,
    # Local focus thresholds on the contrast-normalized sharpness of the primary subject.
    # Run `photosort calibrate` to see your set's distribution and adjust.
    "focus": {"tier2_min": 0.030, "tier1_min": 0.010},
    # Cloud stage
    "backend": "gemini",
    "model": None,               # None = backend default
    "batch_size": 1000,          # requests per batch job (Anthropic backend clamps to its 256 MB limit)
    "gemini_thinking_level": "LOW",
    "anthropic_effort": "low",
    "max_output_tokens": 1024,
    "ollama_num_ctx": 8192,
    "ollama_num_predict": 1536,      # constrained JSON is pretty-printed; leave headroom so it never truncates
    "ollama_frame_long_edge": 1024,
    "ollama_schema_max_lengths": True,   # hard string caps in the grammar (safer, slower decode); False = rely on sampling  # the local model gets a smaller frame; the crop carries fine focus
    # Sorting
    "focus_source": "vlm",       # vlm | local | strict (strict = min of both)
}


def load(workdir: Path) -> dict:
    cfg = json.loads(json.dumps(DEFAULTS))
    p = workdir / "config.json"
    if p.exists():
        user = json.loads(p.read_text())
        for k, v in user.items():
            if isinstance(v, dict) and isinstance(cfg.get(k), dict):
                cfg[k].update(v)
            else:
                cfg[k] = v
    else:
        workdir.mkdir(parents=True, exist_ok=True)
        p.write_text(json.dumps(cfg, indent=2))
    return cfg
