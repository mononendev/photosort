"""photosort: two-stage photo culling/tagging harness.

Stage 1 (local, free): person detection + full-resolution sharpness scoring.
Stage 2 (cloud, batch): a vision model tags subject, composition, keywords, and
cross-checks focus using the downscaled frame plus a native-resolution subject crop.
"""
__version__ = "0.1.0"
