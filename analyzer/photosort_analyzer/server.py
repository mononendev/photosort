"""HTTP surface for the Go backend. Loopback only (a sidecar in the API pod, or a child process of the CLI); nothing
here is exposed to users, and every path it is given was already checked against the photos root by the backend."""
from __future__ import annotations
import os
import platform
from pathlib import Path

from fastapi import FastAPI, HTTPException, Response
from fastapi.responses import JSONResponse

from . import __version__, debugviz, detectors, measure as MS
from . import images as I

app = FastAPI(title="photosort analyzer", version=__version__, docs_url=None, redoc_url=None)


def _missing(path: str):
    if not Path(path).is_file():
        raise HTTPException(404, f"no such file: {path}")


@app.exception_handler(MS.Gone)
def _gone(_, e):
    return JSONResponse({"detail": f"measurement {e.args[0]} expired; measure again"}, status_code=410)


@app.exception_handler(ValueError)
def _bad(_, e):
    return JSONResponse({"detail": str(e)}, status_code=400)


@app.get("/health")
def health():
    return {"ok": True, "version": __version__, "python": platform.python_version(), "device": detectors.device(),
            "models": detectors.installed(), "models_dir": os.environ.get("PHOTOSORT_MODELS", str(Path.cwd()))}


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
