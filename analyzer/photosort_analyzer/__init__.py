"""photosort's pixel stage, as a small HTTP service the Go backend drives.

It measures and never decides: decoding and exposure lift, person/pose detection, the sharpness, FFT, noise and
edge-width metrics, face landmarks, and the JPEGs the UI and the vision model see. Picking the primary subject, the
tier rules, and everything stored are the backend's job (internal/rules), so that logic exists once.
"""
import os

__version__ = os.environ.get("PHOTOSORT_VERSION", "dev")
