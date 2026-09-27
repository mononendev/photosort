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

0. **Exposure.** An underexposed frame is brightened first; everything after it (detection, the
   metrics, the vision model's frame and crop, the viewer) sees the lifted image. Brightness is the
   log-average luminance in linear light; below `exposure.raw_dark_key` (RAW) or the stricter
   `jpeg_dark_key` it is raised toward `target_key`, by at most 4 stops for RAW and 1.5 for JPEG, and
   never so far that the brightest 1% pass `highlight_cap` (so night shots lit by fire or stage lights
   stay dark). A RAW is lifted on its embedded camera JPEG rather than re-demosaiced, which keeps the
   in-camera sharpening the thresholds are calibrated on. The lift is a pure gain below a highlight
   shoulder, so the contrast-normalized metrics keep their scale; what changes is that dim regions
   clear the `EPS` contrast floor, which otherwise scores a sharp but dark eye band near zero.
1. **People.** YOLO11n-pose runs on a 1280 px copy of the frame and returns a box and 17 keypoints per
   person. Boxes smaller than 0.15% of the frame are ignored. Head, torso and body regions come from the
   keypoints, so a helmeted head still gets a head box.
2. **Primary subject.** People are ranked by
   `priority = area × (1 − ½·distance from center) × (½ + ½·detector confidence)`. The top one is the
   primary subject and decides the tier. On Canon files, the AF points in the maker notes override this:
   each active point scores 2 on a person's head, 1.5 on the torso, 1 elsewhere on the body, and the
   person with the highest score (at least `af.min_score`) becomes the primary, whatever their size or
   sharpness. A point just beside a region earns up to half its weight, fading to nothing `af.near` (2)
   point-widths away: spot AF often sits a point-width off the head it focused on. CR3 needs `exiftool`
   installed for this.
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

   | Eyes found? | Tier N (3 sharp, 2 slightly soft, 1 soft; else 0 miss) |
   |---|---|
   | yes | Laplacian ≥ `eye_tierN_min` **and** FFT ≥ `hf_tierN_min` (**and** head Laplacian ≥ `tierN_min` for eyewear) |
   | no | head Laplacian ≥ `tierN_min` |

   **Eyewear.** Sunglasses and goggles put hard, high-contrast frame edges in the eye band, which pass the
   Laplacian even when a little soft. When the band's Laplacian is more than `eyewear_ratio` times the head's,
   the head box has to clear the tier as well.

   The tier grades the primary subject, but the primary is a guess. If it grades below `floor_tier` (2)
   while someone else, detected with confidence at least `floor_conf`, grades `floor_grade` (3) or better,
   focus landed on a person, so the frame is raised to `floor_tier` with the reason `secondary_person_sharp`.
   With `floor_tier` off, such a frame stays tier 0 under that reason. No people is tier 0 (`no_people`). The reason is stored with the tier and
   shown in the UI.
7. **Focus plane.** The metrics above grade the subject in absolute terms, so a frame whose focus landed
   just behind the rider can still pass. This step checks that the head is the sharpest thing around it.
   It measures blur as edge width: for a blurred step, the steepest slope divided by the step height gives the
   blur's width in pixels, whatever the contrast or what the edge belongs to. Unlike the Laplacian ratio, that
   lets grass be compared with a face. The head, the torso and the surroundings (within one person-size, outside
   a margin for helmets and limbs the boxes miss) are each measured on their own, from the 90th percentile of
   their strong edges. A tier 3 whose head carries at least `plane_max_extra` px more blur than its surroundings
   (added in quadrature) drops to tier 2 (`sharper_around_subject`). Bokeh with no edges can't trigger it. The
   same comparison against the torso is stored and shown, but only decides when `plane_body_max_extra` is set:
   clothing print is steeper than any face, so it reads high on sharp frames too. Primary subject only.

   **Soft person in front.** The plane check trusts that the primary is the subject. When the AF point slips
   off a rider onto a spectator behind them, the spectator is sharp (and so are their surroundings), while the
   rider is not. So a tier 3 also drops to 2 (`soft_person_in_front`) when another person stands beside the
   primary (at most `front_max_gap` primary-heights apart sideways) and clearly nearer (at least
   `front_min_height` times as tall, feet lower by `front_min_drop` primary-heights), and their head box grades
   `front_max_grade` or worse. Their head box decides, not their eye band, which can grade low on a sharp face.
   People within `front_edge` of the frame edge are passers-by in the foreground and don't count. It uses only
   stored boxes and metrics, so a rescore applies it.

   **Metrics split.** The eye band Laplacian, the eye band FFT ratio and the head box Laplacian each get graded
   on their own thresholds too. They measure overlapping parts of one head, so they should roughly agree. When
   one sits `split_steps` or more tiers from the nearest of the others (say the pose model put the eye band on a
   helmet visor, eyes 1 and FFT 0, while the head box is plainly sharp at 3), a region landed wrong and the tier
   can't be trusted either way. The tier is left alone and it doesn't count toward needs review; it's recorded as
   `split` in the local result, and Review's "local metrics disagree" filter finds these. A rescore applies it.
8. **EXIF prior.** Aperture, shutter, focal length and ISO are read from EXIF. An entrance pupil ≥ 40 mm
   or f/2 and wider flags very shallow depth of field; a shutter at least a stop slower than 1/focal-length
   (35 mm equivalent) or slower than 1/60 s flags motion-blur risk. A tier 3 that clears its thresholds by
   less than 1.5× at a risky shutter speed is demoted to tier 2 (`borderline_sharp_slow_shutter`). A clearly
   sharp subject, such as a well-panned rider, keeps tier 3.
9. **Noise.** Grain that survives the Laplacian's pre-blur reads as detail (on the upscaled dev-data, six levels
   of added noise lift an all-blur frame from tier 0 to 2), and the camera's noise reduction at high ISO smears
   real detail away. So high noise gets the same treatment as a slow shutter: a tier 3 that clears its thresholds
   by less than `noise.margin` (1.5×) drops to tier 2 (`borderline_sharp_noisy`). Noise is judged on effective
   ISO, ISO × 2^lift, since lifting a frame by the exposure step amplifies its noise as shooting that much
   higher would have: `noise.high_iso` (6400) and up is high, `noisy_iso` (1600) and up moderate. The noise is
   also measured on every frame after the lift (Immerkær's estimate over the flattest half of the pixels, in
   8-bit levels) and stored. Set `noise.medium_sigma` and `high_sigma` to judge on it instead, which accounts for
   the sensor and in-camera noise reduction; its level depends on how the file was processed, so the cuts ship
   unset. Moderate and high noise are also described in the vision prompt.

## Calibrating

The shipped thresholds are placeholders. The right values depend on the camera, the lens, and how picky
you are, and the bundled `dev-data` can't set them (it is upscaled, so nothing in it is sharp at native
resolution).

**From your own picks (best).** Rate or color-label a few hundred frames in Lightroom, save the metadata
to XMP, and upload the sidecars (or a `.zip`, or a CSV with `name,rating,label,focus_tier`) on the
Calibrate page. It matches them to tracked images by filename and suggests thresholds for each metric,
with "use all + re-score" to apply them. A `focus:3` keyword or explicit CSV column wins; otherwise color
labels (Blue/Green 3, Yellow 2, Orange 1, Red 0), then stars (4-5 → 3, 3 → 2, 2 → 1, 1 → 0) are used.

**By eye.** The Calibrate page (or `photosort calibrate --metric eye|hf|head`) shows primary subjects
ordered softest to sharpest with their values. Pick the values where a miss becomes soft, soft
becomes slightly soft, and slightly soft becomes sharp, enter them, and re-score. On the page, every metric
is higher-is-sharper: a cut is the minimum score for its tier, so raising it is stricter. Each cut shows the
share of photos that reach it as you type, and markers between the crops move with it.

Every other knob below (and the EXIF, AF, exposure and detection settings) is editable in the page's
"Focus rules and other tuning" section, with which way is stricter and when a change takes effect: on
re-score, on a photo's next local pass, or on its next vision-model tag.

Re-scoring only re-applies thresholds to stored numbers, so it takes seconds. Leaning tier 3 slightly
high is usually right: a false "sharp" costs more than a false "check this".

## Switches

`config.json` → `focus`:

| Key | Default | |
|---|---|---|
| `use_eyes` | `true` | Judge on the eye band when eyes are found |
| `use_hf` | `true` | Require the FFT ratio as well as the Laplacian on the eye band |
| `eye_tier3_min`, `eye_tier2_min`, `eye_tier1_min` | 0.06, 0.035, 0.02 | Eye band Laplacian |
| `hf_tier3_min`, `hf_tier2_min`, `hf_tier1_min` | 0.03, 0.017, 0.01 | Eye band FFT ratio |
| `tier3_min`, `tier2_min`, `tier1_min` | 0.03, 0.017, 0.01 | Head box Laplacian |
| `eyewear_ratio` | 3.0 | Eye band Laplacian over this × the head's counts as eyewear; the head must clear too (`null` = off) |
| `use_plane` | `true` | Run the focus-plane check (needs a local re-analyze; re-scoring alone can't add it) |
| `plane_max_extra` | 0.4 | Extra head blur over the surroundings, in px, that takes a tier 3 to 2 |
| `plane_body_max_extra` | `null` | The same against the torso (off: clothing print reads sharp) |
| `use_front` | `true` | Run the soft-person-in-front check |
| `front_min_height`, `front_min_drop`, `front_max_gap` | 1.0, 0.25, 0.5 | Nearer and beside: height ratio, feet drop and sideways gap, in primary heights |
| `front_max_grade` | 1 | Head box grade at or below which the person in front counts as soft |
| `front_edge` | 0.01 | Ignore people within this fraction of the frame edge |
| `floor_tier` | 2 | Minimum tier when someone besides the primary grades sharp (`null` = off, a miss stays 0) |
| `floor_grade`, `floor_conf` | 3, 0.5 | How sharp that person must grade, and the least detector confidence that counts |
| `split_steps` | 2 | Flags a metrics split when one metric grades this many tiers from the nearest other (`null` = off) |

`exif`: `crop_factor` (for bodies that don't write a 35 mm-equivalent focal length), `wide_open_f`,
`shake_margin`.

`noise`: `noisy_iso`, `high_iso` (effective ISO cuts), `medium_sigma`, `high_sigma` (measured-noise cuts, `null` =
judge on ISO), `margin` (1 = off).
