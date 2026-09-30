"""HTTP surface for the Go backend. Nothing here is exposed to users, and every path it is given was already checked
against the photos root by the backend. It listens on loopback as a sidecar or a child process of the CLI; as a pool
of pods it listens on the pod network, where $PHOTOSORT_PHOTOS_ROOT confines it to the photos (and a NetworkPolicy to
the backend)."""
from __future__ import annotations
import os
import platform
from pathlib import Path

from fastapi import FastAPI, HTTPException, Response
from fastapi.responses import JSONResponse

from . import __version__, debugviz, detectors, measure as MS
from . import images as I

app = FastAPI(title="photosort analyzer", version=__version__, docs_url=None, redoc_url=None)


PHOTOS_ROOT = os.environ.get("PHOTOSORT_PHOTOS_ROOT")


def slots() -> int:
    """How many images the backend should have in flight here at once: $ANALYZER_SLOTS, else one per usable core."""
    if n := int(os.environ.get("ANALYZER_SLOTS", "0")):
        return n
    return len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else (os.cpu_count() or 1)


# Each session holds a decoded full-resolution image (hundreds of MB), and the backend only has `slots` in flight here,
# so anything past a couple per slot is a measure whose finalize never came (the backend failed in between). Evict
# those rather than let them pile up to SESSION_MAX and OOM the pod.
MS.sessions.max = 2 * slots()


def _missing(path: str):
    if PHOTOS_ROOT and not Path(path).resolve().is_relative_to(Path(PHOTOS_ROOT).resolve()):
        raise HTTPException(403, f"outside the photos root: {path}")
    if not Path(path).is_file():
        raise HTTPException(404, f"no such file: {path}")


@app.exception_handler(MS.Gone)
def _gone(_, e):
    return JSONResponse({"detail": f"measurement {e.args[0]} expired; measure again"}, status_code=410)


@app.exception_handler(ValueError)
def _bad(_, e):
    return JSONResponse({"detail": str(e)}, status_code=400)


@app.exception_handler(Exception)
def _failed(_, e):
    # The backend stores this as the image's error, so say what went wrong.
    return JSONResponse({"detail": f"{type(e).__name__}: {e}"}, status_code=500)


@app.get("/health")
def health():
    return {"ok": True, "version": __version__, "python": platform.python_version(), "device": detectors.device(),
            "models": detectors.installed(), "slots": slots(), "models_dir": os.environ.get("PHOTOSORT_MODELS", str(Path.cwd()))}


@app.post("/measure")
def measure(req: dict):
    _missing(req["path"])
    return MS.measure(req)


@app.post("/finalize")
def finalize(req: dict):
    return MS.finalize(req)


@app.post("/detect")
def detect(req: dict):
    _missing(req["path"])
    return MS.detect(req)


@app.post("/render-full")
def render_full(req: dict):
    """The full-resolution viewer image: decoded, oriented and lifted exactly as the metrics saw it."""
    _missing(req["path"])
    return Response(I.to_jpeg(I.load_rgb(Path(req["path"]), req.get("exposure")), req.get("quality", 92)),
                    media_type="image/jpeg")


@app.post("/focus-debug")
def focus_debug(req: dict):
    _missing(req["path"])
    return debugviz.focus_debug(Path(req["path"]), req.get("local") or {}, req.get("exposure"))
