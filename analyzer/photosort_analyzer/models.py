"""The pose model store: $PHOTOSORT_MODELS/pose/<name>.onnx, listed in pose/manifest.json.

Models come in three families, each with its own decoding (see detectors/):

  yolo_nms  YOLO11 / YOLOv8 pose: raw head output, confidence filter + NMS here.
  yolo_e2e  YOLO26 pose: the NMS-free end-to-end head; its top detections come out ready.
  rtmo      MMPose RTMO: one-stage, 640x640, boxes and keypoints as separate outputs.

A YOLO model is converted from its ultralytics .pt by `python -m photosort_analyzer.models export NAME` (needs the
`ultralytics` extra: the exporter image in the cluster, a dev venv locally). RTMO ships as ONNX and is downloaded.
"""
from __future__ import annotations
import argparse
import hashlib
import io
import json
import os
import shutil
import sys
import tempfile
import threading
import urllib.request
import zipfile
from pathlib import Path

from .weights import models_dir

# Everything `export`/`pull` can fetch. imgsz is the detector's input (long edge for YOLO, which letterboxes to a
# multiple of 32 like ultralytics' predict; the fixed square for RTMO).
CATALOG: dict[str, dict] = {
    **{f"yolo11{s}-pose": {"family": "yolo_nms", "imgsz": 1280, "source": f"ultralytics:yolo11{s}-pose.pt",
                           "license": "AGPL-3.0"} for s in "nsmlx"},
    **{f"yolo26{s}-pose": {"family": "yolo_e2e", "imgsz": 1280, "source": f"ultralytics:yolo26{s}-pose.pt",
                           "license": "AGPL-3.0"} for s in "nsmlx"},
    "rtmo-s": {"family": "rtmo", "imgsz": 640, "license": "Apache-2.0",
               "source": "https://download.openmmlab.com/mmpose/v1/projects/rtmo/onnx_sdk/rtmo-s_8xb32-600e_body7-640x640-dac2bf74_20231211.zip"},
    "rtmo-m": {"family": "rtmo", "imgsz": 640, "license": "Apache-2.0",
               "source": "https://download.openmmlab.com/mmpose/v1/projects/rtmo/onnx_sdk/rtmo-m_16xb16-600e_body7-640x640-39e78cc4_20231211.zip"},
    "rtmo-l": {"family": "rtmo", "imgsz": 640, "license": "Apache-2.0",
               "source": "https://download.openmmlab.com/mmpose/v1/projects/rtmo/onnx_sdk/rtmo-l_16xb16-600e_body7-640x640-b37118ce_20231211.zip"},
}

_lock = threading.Lock()


def pose_dir() -> Path:
    d = models_dir() / "pose"
    d.mkdir(parents=True, exist_ok=True)
    return d


def _seed_dir() -> Path:
    from .weights import SEED_WEIGHTS_DIR
    return SEED_WEIGHTS_DIR / "pose"


def manifest() -> dict[str, dict]:
    """Installed models by name: the models volume's manifest over the image's seed copy."""
    out: dict[str, dict] = {}
    for d in (_seed_dir(), pose_dir()):
        p = d / "manifest.json"
        if p.exists():
            try:
                for name, e in json.loads(p.read_text()).items():
                    if (d / e["file"]).exists():
                        out[name] = {**e, "path": str(d / e["file"])}
            except (ValueError, KeyError):
                pass
    return out


def _record(name: str, file: Path, extra: dict):
    with _lock:
        p = pose_dir() / "manifest.json"
        m = json.loads(p.read_text()) if p.exists() else {}
        h = hashlib.sha256(file.read_bytes()).hexdigest()
        m[name] = {**CATALOG.get(name, {}), **extra, "file": file.name, "sha256": h, "bytes": file.stat().st_size}
        tmp = p.with_suffix(".tmp")
        tmp.write_text(json.dumps(m, indent=1, sort_keys=True))
        tmp.replace(p)


def export(name: str) -> Path:
    """Convert an ultralytics pose model to ONNX in the store (dynamic input size, so the letterbox can pad to a
    multiple of 32 as ultralytics' own predict does)."""
    spec = CATALOG[name]
    from ultralytics import YOLO
    work = Path(tempfile.mkdtemp())
    try:
        pt = spec["source"].split(":", 1)[1]
        cwd = os.getcwd()
        os.chdir(work)  # ultralytics downloads the .pt into the working directory
        try:
            model = YOLO(pt)
            # nms=False keeps YOLO26's NMS-free one-to-one head (ultralytics' own predict path for it); without it the
            # export falls back to the one-to-many head, which needs NMS like YOLO11.
            kw = {"nms": False} if spec["family"] == "yolo_e2e" else {}
            onnx = Path(model.export(format="onnx", dynamic=True, simplify=True, opset=17, imgsz=spec["imgsz"],
                                     **kw)).resolve()
        finally:
            os.chdir(cwd)
        dest = pose_dir() / f"{name}.onnx"
        shutil.move(str(onnx), dest)
        _record(name, dest, {"exported_with": _ultralytics_version()})
        return dest
    finally:
        shutil.rmtree(work, ignore_errors=True)


def _ultralytics_version() -> str:
    import ultralytics
    return ultralytics.__version__


def pull(name: str) -> Path:
    """Fetch a model that ships as ONNX (RTMO)."""
    spec = CATALOG[name]
    with urllib.request.urlopen(spec["source"], timeout=300) as r:
        data = r.read()
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        member = next(n for n in z.namelist() if n.endswith(".onnx"))
        dest = pose_dir() / f"{name}.onnx"
        dest.write_bytes(z.read(member))
    _record(name, dest, {})
    return dest


def main(argv=None):
    ap = argparse.ArgumentParser(prog="python -m photosort_analyzer.models")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("list", help="installed and available pose models")
    p = sub.add_parser("get", help="install models (export YOLO from ultralytics, download RTMO)")
    p.add_argument("names", nargs="+")
    a = ap.parse_args(argv)
    if a.cmd == "list":
        have = manifest()
        for name, spec in CATALOG.items():
            mark = "installed" if name in have else ""
            print(f"{name:16s} {spec['family']:9s} {spec['imgsz']:5d}  {spec['license']:11s} {mark}")
        return
    for n in a.names:
        if n not in CATALOG:
            sys.exit(f"unknown model {n!r}; see `list`")
        dest = export(n) if CATALOG[n]["source"].startswith("ultralytics:") else pull(n)
        print(f"{n}: {dest}")


if __name__ == "__main__":
    main()
