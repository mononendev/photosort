from __future__ import annotations
import json
import os
import shutil
from pathlib import Path

SEED_WEIGHTS_DIR = Path("/app/weights")   # where the docker image keeps its pre-fetched copy
# Weights that ultralytics can't fetch by name.
WEIGHT_URLS = {
    "face_detection_yunet_2023mar.onnx":
        "https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx",
}


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
    if p.name in WEIGHT_URLS:
        import urllib.request
        tmp = target.with_suffix(target.suffix + ".part")
        urllib.request.urlretrieve(WEIGHT_URLS[p.name], tmp)
        tmp.replace(target)
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
    "face_model": "face_detection_yunet_2023mar.onnx",   # OpenCV YuNet: locates the eyes inside the head box
    "face_conf": 0.6,
    "eye_max_people": 4,         # eye bands for the N most prominent people
    # Local focus thresholds. When the eyes are found (face landmarks, else confident pose keypoints), the
    # eye band must clear both eye_* (contrast-normalized Laplacian) and hf_* (FFT upper-mid band energy
    # ratio); otherwise the head box Laplacian is judged against tier*_min. Tiers: 3 sharp, 2 slightly soft,
    # 1 soft, 0 miss. The eye/hf values are placeholders: upload exported verdicts on the Calibrate page (or run
    # `photosort calibrate`) to set them.
    "focus": {"tier3_min": 0.030, "tier2_min": 0.017, "tier1_min": 0.010,
              "eye_tier3_min": 0.060, "eye_tier2_min": 0.035, "eye_tier1_min": 0.020,
              "hf_tier3_min": 0.030, "hf_tier2_min": 0.017, "hf_tier1_min": 0.010,
              "use_eyes": True, "use_hf": True},
    # Camera-metadata prior: crop_factor converts focal length to 35mm-equivalent when EXIF lacks it;
    # f-number <= wide_open_f or entrance pupil >= 40mm = "very shallow DOF"; a tier-3 sharpness below tier3_min*shake_margin is
    # demoted to tier 2 when the shutter was slow enough that motion blur is likely.
    "exif": {"crop_factor": 1.0, "wide_open_f": 2.0, "action_shutter": 1 / 500, "shake_margin": 1.5},
    # Camera AF points (Canon maker notes): the person the active points land on becomes the primary subject,
    # whatever their size or sharpness, when their score (head hit 2, torso 1.5, body 1 per point) >= min_score.
    # y_up: AF y offsets count upward from center (flip if boxes draw mirrored top-to-bottom on your body).
    "af": {"use": True, "min_score": 0.5, "y_up": True},
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
    "truth": {"label_tiers": {"Blue": 3, "Green": 3, "Yellow": 2, "Orange": 1, "Red": 0},
              "rating_tiers": {"5": 3, "4": 3, "3": 2, "2": 1, "1": 0, "0": None}},
    # Sorting
    "focus_source": "vlm",       # vlm | local | strict (strict = min of both)
}


def _migrate_tiers(user: dict) -> bool:
    """Bring a config.json from the three-tier scale (0 none, 1 partial, 2 sharp) to the four-tier one (0 miss,
    1 soft, 2 slightly soft, 3 sharp). Old tier 2 becomes 3 and old tier 1 becomes 2, as in the database; each metric's
    new tier-2 cut starts at the geometric mean of its old two cuts. Returns whether anything changed."""
    changed = False
    f = user.get("focus")
    if isinstance(f, dict):
        for pre in ("", "eye_", "hf_"):
            t2, t1 = f.get(f"{pre}tier2_min"), f.get(f"{pre}tier1_min")
            if t2 is not None and f"{pre}tier3_min" not in f:
                f[f"{pre}tier3_min"] = t2
                f[f"{pre}tier2_min"] = round((t2 * t1) ** 0.5, 4) if t1 and t2 > 0 and t1 > 0 else t2
                changed = True
    t = user.get("truth")
    if changed and isinstance(t, dict):
        up = {1: 2, 2: 3}
        for k in ("label_tiers", "rating_tiers"):
            if isinstance(t.get(k), dict):
                t[k] = {name: up.get(v, v) for name, v in t[k].items()}
        if isinstance(t.get("label_tiers"), dict):
            t["label_tiers"].setdefault("Orange", 1)
        if t.get("rating_tiers") == {"5": 3, "4": 3, "3": 2, "2": 2, "1": 0, "0": None}:   # the old default
            t["rating_tiers"] = dict(DEFAULTS["truth"]["rating_tiers"])
    return changed


def load(workdir: Path) -> dict:
    cfg = json.loads(json.dumps(DEFAULTS))
    p = workdir / "config.json"
    if p.exists():
        user = json.loads(p.read_text())
        if _migrate_tiers(user):
            p.write_text(json.dumps(user, indent=2))
        for k, v in user.items():
            if isinstance(v, dict) and isinstance(cfg.get(k), dict):
                cfg[k].update(v)
            else:
                cfg[k] = v
    else:
        workdir.mkdir(parents=True, exist_ok=True)
        p.write_text(json.dumps(cfg, indent=2))
    return cfg
