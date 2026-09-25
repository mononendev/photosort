# How focus is measured

The local stage decides whether a frame is sharp where it matters, before any vision model sees it. It
runs on the original pixels, costs nothing per image, and its numbers are handed to the vision model as
context. Code: [`photosort/local.py`](../photosort/local.py).

## Why the eyes

At f/1.4-f/2 the depth of field on a person a few metres away is a few centimetres. A frame can have a
tack-sharp shoulder and soft eyes, and a whole-image sharpness score can't tell those apart; neither can a
vision model looking at a 1568 px downscale of a 20 MP frame. So photosort measures the eyes of the main
subject at native resolution, and falls back to the head when the eyes can't be seen.

## Steps

1. **People.** YOLO11n-pose runs on a 1280 px copy of the frame and returns a box and 17 keypoints per
   person. Boxes smaller than 0.15% of the frame are ignored. Head, torso and body regions come from the
   keypoints, so a helmeted head still gets a head box.
2. **Primary subject.** People are ranked by
   `priority = area × (1 − ½·distance from center) × (½ + ½·detector confidence)`. The top one is the
   primary subject and decides the tier. On Canon files, the AF points in the maker notes override this:
   each active point scores 2 on a person's head, 1.5 on the torso, 1 elsewhere on the body, and the
   person with the highest score (at least `af.min_score`) becomes the primary, whatever their size or
   sharpness. CR3 needs `exiftool` installed for this.
3. **Eyes.** OpenCV's [YuNet](https://github.com/opencv/opencv_zoo/tree/main/models/face_detection_yunet)
   face detector runs on a native-resolution window around each head box (the four most prominent
   people). If it finds a face, its eye landmarks are used; otherwise the pose model's eye keypoints are,
   when both have confidence ≥ 0.5.
4. **Eye band.** A box across both eyes, extended by half the inter-eye distance on each side and 0.4 of
   it above and below, to take in lids and lashes.
5. **Metrics**, each cut from the native-resolution grayscale image:
   - **Laplacian:** variance of the Laplacian after a σ=1 Gaussian (to keep high-ISO noise from reading
     as detail), divided by the region's own variance so a low-contrast sharp region scores like a
     high-contrast one. Computed for the eye band, head, torso, body, and background.
   - **FFT ratio** (eye band only): Hann-windowed 2D FFT; energy between 0.25 and 0.75 of Nyquist divided
     by energy above 0.03. On natural patches, a σ=0.8 blur keeps ~45% of this ratio but ~62% of the
     Laplacian, so it separates "slightly soft" from "sharp" better. It flattens under heavy noise,
     which is why the tier needs both metrics.
6. **Tier** for the primary subject:

   | Eyes found? | Tier 2 | Tier 1 |
   |---|---|---|
   | yes | Laplacian ≥ `eye_tier2_min` **and** FFT ≥ `hf_tier2_min` | Laplacian ≥ `eye_tier1_min` **and** FFT ≥ `hf_tier1_min` |
   | no | head Laplacian ≥ `tier2_min` | head Laplacian ≥ `tier1_min` |

   If the primary subject is tier 0 but someone else in the frame grades tier 2, the frame is tier 1
   (`secondary_person_sharp`). No people is tier 0 (`no_people`). The reason is stored with the tier and
   shown in the UI.
7. **EXIF prior.** Aperture, shutter, focal length and ISO are read from EXIF. An entrance pupil ≥ 40 mm
   or f/2 and wider flags very shallow depth of field; a shutter at least a stop slower than 1/focal-length
   (35 mm equivalent) or slower than 1/60 s flags motion-blur risk. A tier 2 that clears its thresholds by
   less than 1.5× at a risky shutter speed is demoted to tier 1 (`borderline_sharp_slow_shutter`). A clearly
   sharp subject, such as a well-panned rider, keeps tier 2.

## Calibrating

The shipped thresholds are placeholders. The right values depend on the camera, the lens, and how picky
you are, and the bundled `dev-data` can't set them (it is upscaled, so nothing in it is sharp at native
resolution).

**From your own picks (best).** Rate or color-label a few hundred frames in Lightroom, save the metadata
to XMP, and upload the sidecars (or a `.zip`, or a CSV with `name,rating,label,focus_tier`) on the
Calibrate page. It matches them to tracked images by filename and suggests thresholds for each metric,
with "use all + re-score" to apply them. A `focus:2` keyword or explicit CSV column wins; otherwise color
labels (Green 2, Yellow 1, Red 0), then stars (4-5 → 2, 2-3 → 1, 1 → 0) are used.

**By eye.** The Calibrate page (or `photosort calibrate --metric eye|hf|head`) shows primary subjects
ordered softest to sharpest with their values. Pick the value where "soft" becomes "usable" and where
"usable" becomes "crisp", enter them, and re-score.

Re-scoring only re-applies thresholds to stored numbers, so it takes seconds. Leaning tier 2 slightly
high is usually right: a false "sharp" costs more than a false "check this".

## Switches

`config.json` → `focus`:

| Key | Default | |
|---|---|---|
| `use_eyes` | `true` | Judge on the eye band when eyes are found |
| `use_hf` | `true` | Require the FFT ratio as well as the Laplacian on the eye band |
| `eye_tier2_min`, `eye_tier1_min` | 0.06, 0.02 | Eye band Laplacian |
| `hf_tier2_min`, `hf_tier1_min` | 0.03, 0.01 | Eye band FFT ratio |
| `tier2_min`, `tier1_min` | 0.03, 0.01 | Head box Laplacian |

`exif`: `crop_factor` (for bodies that don't write a 35 mm-equivalent focal length), `wide_open_f`,
`shake_margin`.
