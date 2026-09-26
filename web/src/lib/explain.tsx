/**
 * Plain-language explanations for the numbers and verdicts the UI shows. The local-tier walk-through mirrors
 * photosort/local.py (_grade / local_tier), so keep the two in step when the tier rule changes.
 */
import type { ReactNode } from 'react';
import type { LocalResult, Person } from '../api/client';
import CheckTable from '../components/CheckTable';
import type { Check } from '../components/CheckTable';

export type Cfg = Record<string, unknown> | undefined;
type Thr = Record<string, number | boolean>;

export const focusThr = (cfg: Cfg): Thr => (cfg?.focus as Thr) ?? {};
const focusSource = (cfg: Cfg): string => (cfg?.focus_source as string) ?? 'vlm';

export const TIER_MEANING: Record<number, string> = {
  3: "sharp: the primary person's head (eyes, face, or helmet edges) is crisply in focus",
  2: "soft: the primary person's head is nearly in focus but not crisp: slightly soft, just off the eyes, or slight motion blur",
  1: 'partial: focus landed on part of the subject (torso, board, hands) or on someone other than the primary person',
  0: 'miss: nobody in focus: no people, everyone blurry, or focus landed on the background or foreground',
};

const REASON_TEXT: Record<string, string> = {
  no_people: 'the pose model found no person big enough to judge',
  subject_too_small: 'a person was found, but every region was too small to measure',
  primary_eyes_sharp: "the primary subject's eye band cleared both tier-3 thresholds",
  primary_head_sharp: "no eyes were located, and the primary subject's head box cleared the tier-3 threshold",
  borderline_sharp_slow_shutter: 'it cleared tier 3, but only barely, at a shutter speed slow enough for motion blur, so it was demoted',
  primary_eyes_soft: "the primary subject's eye band cleared tier 2 but not tier 3",
  primary_soft: "the primary subject's head box cleared tier 2 but not tier 3 (no eyes located)",
  primary_eyes_partial: "the primary subject's eye band cleared tier 1 but not tier 2",
  primary_partial: "the primary subject's head box cleared tier 1 but not tier 2 (no eyes located)",
  secondary_person_sharp: 'the primary subject is soft, but someone else in the frame is sharp',
  nothing_sharp: 'nobody cleared the tier-1 threshold',
};


/** One metric's tier-1, tier-2 and tier-3 cuts from config.focus; prefix is '', 'eye_' or 'hf_'. */
export const tierCuts = (thr: Thr, prefix: string): [number, number, number] =>
  [1, 2, 3].map((n) => thr[`${prefix}tier${n}_min`] as number) as [number, number, number];

/** Which region and thresholds decide this person's grade, as local.py's _grade picks them. */
export function gradeBasis(p: Person, thr: Thr): { onEyes: boolean; checks: Check[] } {
  const onEyes = thr.use_eyes !== false && p.sharp_eye != null && 'eye_tier3_min' in thr;
  if (onEyes) {
    const checks: Check[] = [{ label: 'eye band Laplacian', value: p.sharp_eye, t: tierCuts(thr, 'eye_') }];
    if (thr.use_hf !== false && p.hf_eye != null && 'hf_tier3_min' in thr) {
      checks.push({ label: 'eye band FFT ratio', value: p.hf_eye, t: tierCuts(thr, 'hf_') });
    }
    return { onEyes, checks };
  }
  const s = p.sharp_head ?? p.sharp_body;
  return { onEyes, checks: [{ label: p.sharp_head != null ? 'head box Laplacian' : 'body box Laplacian', value: s, t: tierCuts(thr, '') }] };
}

/** Step-by-step reasoning for the local tier of one image. */
export function explainLocal(l: LocalResult, cfg: Cfg): ReactNode {
  const thr = focusThr(cfg);
  const p = l.people?.[0];
  const reason = REASON_TEXT[l.local_reason] ?? l.local_reason;
  if (!p) return <div><b>Local tier {l.local_tier}</b>: {reason}.</div>;
  const { onEyes, checks } = gradeBasis(p, thr);
  const margin = ((cfg?.exif as Thr | undefined)?.shake_margin as number) ?? 1.5;
  return (
    <div className="space-y-1">
      <div><b>Local tier {l.local_tier}</b> ({l.local_reason}): {reason}.</div>
      <div className="text-gray-400">
        {onEyes
          ? <>Judged on the band across both eyes, found by {p.eye_src === 'face' ? 'the face-landmark model' : "the pose model's eye keypoints (no face found)"}. Both metrics must clear a threshold for that tier.</>
          : <>No eyes located (helmet, visor, turned away, or too small), so the head box decides on its own thresholds.</>}
      </div>
      <CheckTable checks={checks} />
      {l.local_reason === 'borderline_sharp_slow_shutter' && (
        <div className="text-gray-400">Tier 3 at a slow shutter must clear {margin}× the tier-3 thresholds; this one didn't, so it's soft (2).</div>
      )}
      {l.local_reason === 'secondary_person_sharp' && (
        <div className="text-gray-400">Another person's own grade was tier 3, which lifts a missed frame to partial (1), because they aren't the main subject.</div>
      )}
      <div className="text-gray-500">Thresholds are set on the Calibrate page. The primary subject is the largest, most central, most confident person.</div>
    </div>
  );
}

export function explainFinal(d: { focus_tier?: number | null; focus_tier_local?: number | null; focus_tier_vlm?: number | null; overridden?: boolean; override?: { focus_tier?: number } | null }, cfg: Cfg): ReactNode {
  const src = focusSource(cfg);
  const rule = src === 'local' ? 'the local tier' : src === 'strict' ? 'the lower of the local and model tiers' : 'the vision model tier, or the local tier until the model has run';
  return (
    <div className="space-y-1">
      {d.focus_tier != null && <div><b>Focus {d.focus_tier}</b>: {TIER_MEANING[d.focus_tier]}.</div>}
      <div className="text-gray-400">
        {d.override?.focus_tier != null
          ? <>This is your call, which beats everything.</>
          : <>Comes from {rule} (focus_source = {src}).</>}
        {' '}Local {d.focus_tier_local ?? '–'}, model {d.focus_tier_vlm ?? '–'}.
      </div>
      <div className="text-gray-500">This tier sorts the export tree (focus_N/…) and is written into the XMP keywords.</div>
    </div>
  );
}

export function explainDisagree(l: LocalResult, vlmTier: number, notes: string | undefined, cfg: Cfg): ReactNode {
  const src = focusSource(cfg);
  return (
    <div className="space-y-1">
      <div><b>Local says {l.local_tier}</b>, because {REASON_TEXT[l.local_reason] ?? l.local_reason}.</div>
      <div><b>Model says {vlmTier}</b>{notes ? <>: “{notes}”</> : null}</div>
      <div className="text-gray-400">
        They measure different things. The local tier is arithmetic on native pixels against thresholds. The model looks at the crop and can
        tell motion blur from missed focus, but it may be generous about slight softness.
        {' '}With focus_source = {src}, {src === 'local' ? 'the local tier' : src === 'strict' ? 'the lower one' : "the model's tier"} is what's shown.
      </div>
      <div className="text-gray-500">Flagged for review: it appears under “needs review” and in the export's review/ folder. Setting your call settles it.</div>
    </div>
  );
}

export const METRIC_TIPS = {
  eyes: <>Contrast-normalized Laplacian variance on a band across both eyes at native resolution: edge energy after a light denoise, divided by the region's contrast. Higher is sharper. This is the region you judge by at 100% in Lightroom.</>,
  fft: <>Share of the eye band's spectral energy in the upper-middle frequencies (0.25–0.75 of Nyquist). It drops faster than the Laplacian for the first bit of missed focus. The very top of the spectrum, where high-ISO noise lives, is left out.</>,
  eyeSrc: {
    face: <>The eyes were located by a face-landmark model (OpenCV YuNet), run on a native-resolution window around the head. This is the precise path.</>,
    pose: <>The face model found no face, so the eyes come from the pose model's eye keypoints (confidence ≥ 0.5), mapped from the detection size. Coarser, but still the right area.</>,
  } as Record<string, ReactNode>,
  noEyes: <>No eyes were located: helmet or visor, head turned away, sunglasses plus a small face, or the face too small (inter-eye distance &lt; 8 px). The head box decides instead.</>,
  head: <>Same Laplacian metric on a square box around the head keypoints. It includes hair, helmet, ears and background, so at shallow depth of field it can read soft even when the eyes are sharp, or the reverse.</>,
  torso: <>Laplacian from shoulders to hips. If the torso is much sharper than the eyes or head, focus probably landed on the body or board.</>,
  body: <>Laplacian on the whole person box. Mostly useful when the head is too small to measure.</>,
  bg: <>Laplacian on the whole frame with every person masked out, downscaled. A background sharper than the subject suggests focus landed behind them.</>,
  headSrc: {
    keypoints: <>The head box is centered on the nose, eye and ear keypoints from YOLO pose, sized from the shoulder width.</>,
    box_top: <>No head keypoints were confident, so the head box assumes an upright person and uses the top of their box.</>,
  } as Record<string, ReactNode>,
  people: <>People found by YOLO pose, ignoring anyone smaller than min_person_frac of the frame. Eye bands are measured for the most prominent few (eye_max_people).</>,
};

export function explainPrior(pr: NonNullable<LocalResult['exif_prior']>, cfg: Cfg): { dof: ReactNode; motion: ReactNode } {
  const ex = (cfg?.exif as Thr | undefined) ?? {};
  return {
    dof: (
      <>Entrance pupil {pr.pupil_mm ?? '–'} mm (focal length ÷ f-number). Pupil ≥ 40 mm or f ≤ {String(ex.wide_open_f ?? 2)} means a razor-thin focus plane.
        Eyes and ears can sit in different planes, so the eye band is what counts. The prior is context for the model; it doesn't change the local tier.</>
    ),
    motion: (
      <>Shutter is {pr.shake_stops != null ? `${pr.shake_stops > 0 ? '+' : ''}${pr.shake_stops} stops` : 'too slow'} relative to the 1/focal-length rule
        (≥ +1 stop, or 1/60 s or slower, is high risk). A tier-3 subject here needs {String(ex.shake_margin ?? 1.5)}× the tier-3 thresholds, or it's demoted to tier 2.</>
    ),
  };
}
