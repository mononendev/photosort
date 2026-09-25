from __future__ import annotations
import json
import os
import shutil
from pathlib import Path

SEED_WEIGHTS_DIR = Path("/app/weights")   # where the docker image keeps its pre-fetched copy


def models_dir() -> Path:
    d = Path(os.environ.get("PHOTOSORT_MODELS", "")) if os.environ.get("PHOTOSORT_MODELS") else Path.cwd()
    d.mkdir(parents=True, exist_ok=True)
    return d


def weights_path(name: str) -> Path:
    """Resolve a weights file inside the models dir, seeding it from the image copy or a download.

    Order: already on the models volume -> copy from the image's /app/weights -> ultralytics download
    (needs egress). Returns the path to hand to YOLO()."""
    p = Path(name)
    if p.is_absolute():
        return p
    target = models_dir() / p.name
    if target.exists():
        return target
    seed = SEED_WEIGHTS_DIR / p.name
    if seed.exists():
        shutil.copy2(seed, target)
        return target
    try:
        from ultralytics.utils.downloads import attempt_download_asset
        attempt_download_asset(str(target))
    except Exception:
        pass  # YOLO() will raise a clear error if the file is still missing
    return target

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
    # Camera-metadata prior: crop_factor converts focal length to 35mm-equivalent when EXIF lacks it;
    # f-number <= wide_open_f or entrance pupil >= 40mm = "very shallow DOF"; a tier-2 sharpness below tier2_min*shake_margin is
    # demoted to tier 1 when the shutter was slow enough that motion blur is likely.
    "exif": {"crop_factor": 1.0, "wide_open_f": 2.0, "action_shutter": 1 / 500, "shake_margin": 1.5},
    # Cloud stage
    "backend": "ollama",         # ollama (local, free) | gemini | anthropic
    "model": None,               # None = backend default
    "vlm_concurrency": 1,
    "batch_size": 1000,          # requests per batch job (Anthropic backend clamps to its 256 MB limit)
    "gemini_thinking_level": "LOW",
    "anthropic_effort": "low",
    "max_output_tokens": 1024,
    "ollama_num_ctx": 8192,
    "ollama_num_predict": 1536,      # constrained JSON is pretty-printed; leave headroom so it never truncates
    "ollama_frame_long_edge": 1024,
    "ollama_schema_max_lengths": True,   # hard string caps in the grammar (safer, slower decode); False = rely on sampling  # the local model gets a smaller frame; the crop carries fine focus
    # Ground truth (calibration): how your exported verdicts map to focus tiers when no explicit tier is given
    "truth": {"label_tiers": {"Green": 2, "Yellow": 1, "Red": 0},
              "rating_tiers": {"5": 2, "4": 2, "3": 1, "2": 1, "1": 0, "0": None}},
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
