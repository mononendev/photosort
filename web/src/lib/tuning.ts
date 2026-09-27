/**
 * Every tuning knob in config.json that the Calibrate page edits, with what it does, which way is stricter and when
 * a change takes effect. The focus tier cuts (tier*_min, eye_tier*_min, hf_tier*_min) have their own panel; any
 * config key not listed here or there still shows under "other settings", so nothing is hidden.
 */

/** When a saved change shows up: re-score (seconds, from stored metrics), a fresh local pass, a vision re-tag,
 * right away (read at query time), or never on the tier (only the notes shown and the vision prompt). */
export type Applies = 'rescore' | 'reanalyze' | 'retag' | 'live' | 'info';

export const APPLIES_TEXT: Record<Applies, { short: string; tip: string }> = {
  rescore: { short: 're-score', tip: 'Recomputed from the stored measurements: saving re-scores the whole library in seconds.' },
  reanalyze: { short: 're-analyze', tip: 'Changes what the local stage measures, so it only reaches a photo on its next local pass (Browse → re-analyze local).' },
  retag: { short: 're-tag', tip: 'Changes what the vision model is sent, so it only reaches a photo when the model tags it again.' },
  live: { short: 'immediate', tip: 'Read whenever tiers are shown or exported: takes effect as soon as it is saved.' },
  info: { short: 'notes only', tip: "Doesn't move the local tier: it only changes the camera notes in the detail view and what the vision prompt is told." },
};

export interface Param {
  /** Dotted config path, e.g. "focus.plane_max_extra" or "detect_conf". */
  key: string;
  label: string;
  kind: 'number' | 'bool' | 'select';
  /** A number that can be switched off (stored as null). */
  nullable?: boolean;
  /** The value a switched-off knob starts from when turned back on. */
  whenOn?: number;
  options?: { value: string | number; label: string }[];
  step?: number;
  min?: number;
  max?: number;
  unit?: string;
  /** Which way makes the local tier pickier, if the knob has a direction at all. */
  stricter?: 'higher' | 'lower';
  /** What raising the value does, in plain words (shown next to the input). */
  up?: string;
  help: string;
  applies: Applies;
  /** Shown instead of the raw number (and parsed back), e.g. shutter speeds as 1/500. */
  display?: 'shutter';
}

export interface Group { title: string; intro: string; params: Param[] }

export const GROUPS: Group[] = [
  {
    title: 'Which measurement decides',
    intro: 'Where the eyes can be located, the band across them decides the tier; otherwise the head box does.',
    params: [
      { key: 'focus.use_eyes', label: 'judge on the eye band', kind: 'bool', applies: 'rescore',
        help: 'Grade on the band across both eyes whenever they were located. Off: every photo is graded on its head box, which is blunter (hair, helmets and background count too).' },
      { key: 'focus.use_hf', label: 'require the FFT ratio too', kind: 'bool', applies: 'rescore',
        help: 'On the eye band, the FFT detail ratio must clear its cut as well as the Laplacian. It drops faster than the Laplacian on slight softness, so leaving it on is stricter.' },
      { key: 'focus.eyewear_ratio', label: 'eyewear ratio', kind: 'number', nullable: true, whenOn: 3, step: 0.25, min: 1, unit: '×', stricter: 'lower', applies: 'rescore',
        up: 'fewer eye bands count as eyewear',
        help: 'An eye band whose Laplacian is over this many times the head box’s reads as sunglasses or goggles: their hard frame edges pass even when soft, so the head box must clear the tier as well. Lower flags more bands as eyewear (stricter); off never does.' },
    ],
  },
  {
    title: 'Focus plane: sharper around the subject',
    intro: 'Catches focus that landed just in front of or behind the primary subject: things right around their head are crisper than the head itself. Only ever takes a tier 3 down to 2.',
    params: [
      { key: 'focus.use_plane', label: 'run the check', kind: 'bool', applies: 'rescore',
        help: 'Compare edge blur on the head with its surroundings. The measurements come from the local pass; the cut below applies on re-score.' },
      { key: 'focus.plane_max_extra', label: 'head vs surroundings', kind: 'number', nullable: true, whenOn: 0.4, step: 0.05, min: 0, unit: 'px', stricter: 'lower', applies: 'rescore',
        up: 'the head may be blurrier than its surroundings before a tier 3 drops',
        help: 'How much more edge blur (px, added in quadrature) the head may carry than everything around it before a tier 3 drops to 2 (sharper_around_subject). Lower is stricter: smaller focus misses count. Too low and ordinary texture differences start demoting sharp frames.' },
      { key: 'focus.plane_body_max_extra', label: 'head vs torso', kind: 'number', nullable: true, whenOn: 0.5, step: 0.05, min: 0, unit: 'px', stricter: 'lower', applies: 'rescore',
        up: 'the head may be blurrier than the torso before a tier 3 drops',
        help: 'The same against the subject’s own torso (sharper_body_than_head). Off by default: clothing print reads sharper than any face, even with both in focus.' },
    ],
  },
  {
    title: 'Soft person in front',
    intro: 'Catches an AF point that slipped past the rider onto someone behind them: the primary is sharp, but a person standing right beside them and nearer the camera is soft. Only ever takes a tier 3 down to 2 (soft_person_in_front). People cut off by the frame edge never count.',
    params: [
      { key: 'focus.use_front', label: 'run the check', kind: 'bool', applies: 'rescore', help: 'Look for a soft person standing just in front of the primary subject.' },
      { key: 'focus.front_min_height', label: 'at least this tall', kind: 'number', step: 0.05, min: 0, unit: '× primary', stricter: 'lower', applies: 'rescore',
        up: 'only much bigger people count as in front',
        help: 'The person in front must be at least this many times the primary’s box height (nearer things look bigger). Lower lets smaller people count, which is stricter.' },
      { key: 'focus.front_min_drop', label: 'feet this much lower', kind: 'number', step: 0.05, min: 0, unit: '× primary height', stricter: 'lower', applies: 'rescore',
        up: 'only people clearly nearer count',
        help: 'Their feet (box bottom) must sit at least this far below the primary’s, in primary heights: on the ground, nearer means lower in the frame. Lower is stricter.' },
      { key: 'focus.front_max_gap', label: 'at most this far to the side', kind: 'number', step: 0.1, min: 0, unit: '× primary height', stricter: 'higher', applies: 'rescore',
        up: 'people farther to the side count too',
        help: 'How far apart sideways the two boxes may be, in primary heights (0 = touching or overlapping). A slipped AF point lands just beside the rider. Higher is stricter, but brings in bystanders.' },
      { key: 'focus.front_max_grade', label: 'counts as soft at', kind: 'select', stricter: 'higher', applies: 'rescore',
        options: [{ value: 0, label: 'tier 0 (miss) only' }, { value: 1, label: 'tier 1 (soft) or worse' }, { value: 2, label: 'tier 2 (slightly soft) or worse' }],
        help: 'How soft the person in front’s head box must be, on the head-box cuts. Their eye band is ignored: it can grade low on a plainly sharp face. A higher setting is stricter.' },
      { key: 'focus.front_edge', label: 'ignore within this of the edge', kind: 'number', step: 0.005, min: 0, unit: '× frame', stricter: 'lower', applies: 'rescore',
        up: 'more people near the edge are ignored as passers-by',
        help: 'People whose box comes within this fraction of the left, right or bottom edge are passers-by in the foreground, not the subject. Lower is stricter.' },
    ],
  },
  {
    title: 'Someone else sharp',
    intro: 'The tier grades the primary subject, but that pick is a guess (the most prominent person, or whoever the AF point landed on). When someone else in the frame is confidently detected and plainly sharp, focus landed on a person, so a lower tier is raised to this minimum (secondary_person_sharp).',
    params: [
      { key: 'focus.floor_tier', label: 'raise to at least', kind: 'number', nullable: true, whenOn: 2, step: 1, min: 1, max: 3, unit: 'tier', stricter: 'lower', applies: 'rescore',
        up: 'more frames with a sharp bystander count as keepers',
        help: 'The minimum tier for a frame where someone besides the primary grades sharp. Off keeps it a miss (tier 0). 3 overrides the checks for focus that went past the subject onto someone behind, so 2 is the useful setting.' },
      { key: 'focus.floor_grade', label: 'when they grade', kind: 'select', stricter: 'higher', applies: 'rescore',
        options: [{ value: 3, label: 'tier 3 (sharp)' }, { value: 2, label: 'tier 2 (slightly soft) or better' }],
        help: 'How sharp the other person must be, on the same cuts as the primary. Higher is stricter.' },
      { key: 'focus.floor_conf', label: 'detector confidence at least', kind: 'number', step: 0.05, min: 0, max: 1, stricter: 'higher', applies: 'rescore',
        up: 'only surer detections can raise a frame',
        help: 'Only people the pose model is at least this sure of count, so a sharp poster or statue doesn’t lift a miss. Higher is stricter.' },
    ],
  },
  {
    title: 'Metrics split',
    intro: 'The eye band Laplacian, eye band FFT ratio and head box Laplacian are each graded on their own cuts. They measure the same head, so one far from the others usually means a box landed wrong (an eye band on a visor while the head is sharp). That never moves the tier; it flags the photo for review.',
    params: [
      { key: 'focus.split_steps', label: 'flag at a gap of', kind: 'number', nullable: true, whenOn: 2, step: 1, min: 1, max: 3, unit: 'tiers', applies: 'rescore',
        up: 'fewer photos flagged: only wilder splits',
        help: 'A photo needs review when one metric grades at least this many tiers from the nearest of the others. Lower flags more photos; off never flags.' },
    ],
  },
  {
    title: 'Camera settings (EXIF)',
    intro: 'Shutter, aperture and focal length flag motion-blur and depth-of-field risk. Only the motion-blur risk can move a tier: a borderline tier 3 at a risky shutter speed drops to 2.',
    params: [
      { key: 'exif.shake_margin', label: 'slow-shutter margin', kind: 'number', step: 0.1, min: 1, unit: '× tier-3 cut', stricter: 'higher', applies: 'rescore',
        up: 'slow-shutter shots must be sharper to keep tier 3',
        help: 'At a shutter slow enough for motion blur (a stop slower than 1/focal length, or 1/60 s and slower), a tier 3 must clear this many times the tier-3 cuts or it drops to 2 (borderline_sharp_slow_shutter). Higher is stricter; 1 turns it off.' },
      { key: 'exif.crop_factor', label: 'crop factor', kind: 'number', step: 0.1, min: 0.5, unit: '×', applies: 'rescore',
        help: 'Converts focal length to 35 mm equivalent for bodies that don’t write it (1.0 full frame, 1.6 Canon APS-C, 1.5 others). Feeds the 1/focal-length rule above.' },
      { key: 'exif.wide_open_f', label: 'shallow depth of field at', kind: 'number', step: 0.1, min: 0.7, unit: 'f/ or wider', applies: 'info',
        help: 'Apertures this wide or wider (or an entrance pupil of 40 mm and up) are noted as very shallow depth of field, so the notes and the vision model are told to judge the eyes.' },
      { key: 'exif.action_shutter', label: 'action shutter', kind: 'number', display: 'shutter', applies: 'info',
        help: 'Slower than this is noted as medium motion risk for moving riders. It shows in the camera notes and the vision prompt; only high risk (a stop past 1/focal length, or 1/60 s) moves the tier.' },
    ],
  },
  {
    title: 'AF points',
    intro: 'Canon maker notes say where the camera focused. The person the active points land on becomes the primary subject, even if someone else is bigger or more central.',
    params: [
      { key: 'af.use', label: 'let AF points pick the primary', kind: 'bool', applies: 'rescore',
        help: 'Off: the primary is always the largest, most central, most confident person.' },
      { key: 'af.min_score', label: 'minimum AF hit score', kind: 'number', step: 0.1, min: 0, applies: 'rescore',
        up: 'the AF points must land more squarely on someone to pick them',
        help: 'Each active point scores by where it lands (head 2, torso 1.5, body 1, scaled by overlap; half that just beside them). A person needs this much to override the prominence pick. Lower trusts the AF points more readily.' },
      { key: 'af.near', label: 'reach beside a person', kind: 'number', step: 0.5, min: 0, unit: 'AF points', stricter: 'lower', applies: 'rescore',
        up: 'points further beside someone still pick them',
        help: 'A point just beside a head or body still earns up to half its weight, fading to nothing this many point-widths away (0 = only points on someone count). Spot AF often sits a point-width off the head it focused on.' },
      { key: 'af.y_up', label: 'AF y counts upward', kind: 'bool', applies: 'reanalyze',
        help: 'How the maker notes’ vertical offsets are read. Flip it only if AF boxes draw mirrored top-to-bottom on your body. Applied when the AF points are read.' },
    ],
  },
  {
    title: 'Exposure lift',
    intro: 'Underexposed frames are brightened before the local stage and the vision model see them. A RAW is lifted from its embedded camera JPEG, which the focus cuts are calibrated on.',
    params: [
      { key: 'exposure.recover', label: 'lift dark frames', kind: 'bool', applies: 'reanalyze', help: 'Off: frames are measured and sent exactly as shot.' },
      { key: 'exposure.target_key', label: 'target brightness', kind: 'number', step: 0.01, min: 0, applies: 'reanalyze',
        up: 'lifted frames come out brighter',
        help: 'Key (log-average luminance, linear light) a lifted frame is brought toward. 0.08 is ordinary daylight.' },
      { key: 'exposure.raw_dark_key', label: 'RAW: lift below', kind: 'number', step: 0.005, min: 0, applies: 'reanalyze',
        up: 'more RAWs get lifted', help: 'A RAW darker than this key gets lifted.' },
      { key: 'exposure.raw_max_ev', label: 'RAW: max lift', kind: 'number', step: 0.5, min: 0, unit: 'EV', applies: 'reanalyze',
        up: 'very dark RAWs can be pushed further (more noise)', help: 'The most a RAW is pushed, in stops. Big pushes raise noise, which reads as detail to the focus metrics.' },
      { key: 'exposure.jpeg_dark_key', label: 'JPEG: lift below', kind: 'number', step: 0.005, min: 0, applies: 'reanalyze',
        up: 'more JPEGs get lifted', help: 'A JPEG darker than this key gets lifted.' },
      { key: 'exposure.jpeg_max_ev', label: 'JPEG: max lift', kind: 'number', step: 0.25, min: 0, unit: 'EV', applies: 'reanalyze',
        up: 'dark JPEGs can be pushed further', help: 'The most a JPEG is pushed, in stops (8-bit files band and clip sooner than RAW).' },
      { key: 'exposure.min_ev', label: 'skip lifts under', kind: 'number', step: 0.1, min: 0, unit: 'EV', applies: 'reanalyze',
        up: 'small lifts are skipped', help: 'A lift smaller than this is not worth doing and is skipped.' },
      { key: 'exposure.highlight_cap', label: 'highlight cap', kind: 'number', step: 0.05, min: 0, max: 1, applies: 'reanalyze',
        up: 'lifts may push highlights closer to clipping', help: 'A lift stops before the 99th-percentile pixel passes this level, so lights and sky don’t clip.' },
    ],
  },
  {
    title: 'People detection',
    intro: 'The pose model finds people and their keypoints; the face model finds the eyes inside each head box.',
    params: [
      { key: 'detect_conf', label: 'person confidence', kind: 'number', step: 0.05, min: 0, max: 1, applies: 'reanalyze',
        up: 'fewer, surer detections', help: 'Pose-model confidence a person needs to count. Lower finds more partly hidden or distant people (and more false ones).' },
      { key: 'min_person_frac', label: 'smallest person', kind: 'number', step: 0.0005, min: 0, unit: '× frame area', applies: 'reanalyze',
        up: 'small, distant people are ignored', help: 'People whose box covers less of the frame than this are ignored.' },
      { key: 'detect_long_edge', label: 'detection resolution', kind: 'number', step: 64, min: 320, unit: 'px', applies: 'reanalyze',
        up: 'finds smaller people, slower', help: 'Long edge the frame is scaled to for the pose model.' },
      { key: 'face_conf', label: 'face confidence', kind: 'number', step: 0.05, min: 0, max: 1, applies: 'reanalyze',
        up: 'eyes located less often (more head-box grading)', help: 'Face-model confidence needed to place the eye band from face landmarks. Below it, confident pose eye keypoints are used, else the head box.' },
      { key: 'eye_max_people', label: 'eye bands for', kind: 'number', step: 1, min: 1, unit: 'people', applies: 'reanalyze',
        up: 'more people get eye measurements, slower', help: 'Eye bands are measured for this many of the most prominent people.' },
      { key: 'dedup_iou', label: 'duplicate box overlap', kind: 'number', step: 0.05, min: 0, max: 1, applies: 'reanalyze',
        up: 'fewer boxes merged', help: 'Two boxes overlapping this much (IoU) are one person; the weaker is dropped.' },
      { key: 'dedup_head_iou', label: 'duplicate overlap, same head', kind: 'number', step: 0.05, min: 0, max: 1, applies: 'reanalyze',
        up: 'fewer boxes merged', help: 'A lower overlap is enough when both boxes put the head in the same place (see next).' },
      { key: 'dedup_head_tol', label: 'same head within', kind: 'number', step: 0.02, min: 0, unit: '× box size', applies: 'reanalyze',
        up: 'heads farther apart still count as the same', help: 'How close the head keypoints must be for two boxes to be the same person.' },
    ],
  },
  {
    title: 'Final tier and the vision model',
    intro: 'Which tier a photo ends up with, and what the vision model is sent.',
    params: [
      { key: 'focus_source', label: 'final tier from', kind: 'select', applies: 'live',
        options: [{ value: 'vlm', label: 'vision model (local while it is pending or stale)' }, { value: 'local', label: 'local tier' }, { value: 'strict', label: 'strict: the lower of the two' }],
        help: 'Your own rating always wins. Strict takes the lower of the local and model tiers.' },
      { key: 'crop_size', label: 'subject crop size', kind: 'number', step: 64, min: 256, unit: 'px', applies: 'retag',
        up: 'the model sees the subject in more detail (more tokens)', help: 'Long edge of the native-resolution crop of the primary subject sent alongside the frame.' },
      { key: 'crop_pad', label: 'subject crop padding', kind: 'number', step: 0.05, min: 0, unit: '× box', applies: 'retag',
        up: 'more context around the subject', help: 'Padding around the person box for that crop.' },
      { key: 'frame_long_edge', label: 'frame size (cloud)', kind: 'number', step: 64, min: 512, unit: 'px', applies: 'retag', help: 'Long edge of the full frame sent to Gemini or Claude.' },
      { key: 'ollama_frame_long_edge', label: 'frame size (local model)', kind: 'number', step: 64, min: 512, unit: 'px', applies: 'retag', help: 'Long edge of the full frame sent to the local Ollama model.' },
    ],
  },
];

/** Keys handled by the focus-threshold panel rather than GROUPS. */
export const THRESHOLD_KEY = /^focus\.(|eye_|hf_)tier[123]_min$/;
/** Plumbing that isn't tuning: never shown under "other settings". */
const PLUMBING = /^(truth\.|detect_model$|face_model$|workers$|backend$|model$|batch_size$|vlm_concurrency$|local_ahead$|gemini_|anthropic_|ollama_(num_|schema)|max_output_tokens$|frame_quality$|crop_quality$)/;

export type Cfg = Record<string, unknown>;

export const getPath = (cfg: Cfg | undefined, key: string): unknown =>
  key.split('.').reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Cfg)[k] : undefined), cfg);

/** Dotted paths of every scalar leaf in a config. */
export function leaves(cfg: Cfg, prefix = ''): string[] {
  return Object.entries(cfg).flatMap(([k, v]) =>
    v && typeof v === 'object' && !Array.isArray(v) ? leaves(v as Cfg, `${prefix}${k}.`) : [`${prefix}${k}`]);
}

/** Config leaves no panel covers, shown as plain inputs so every knob stays reachable. */
export function otherKeys(cfg: Cfg): string[] {
  const known = new Set(GROUPS.flatMap((g) => g.params.map((p) => p.key)));
  return leaves(cfg).filter((k) => !known.has(k) && !THRESHOLD_KEY.test(k) && !PLUMBING.test(k));
}

/** {"focus.a": 1, "b": 2} → {focus: {a: 1}, b: 2}, the shape PUT /api/config merges. */
export function nest(flat: Record<string, unknown>): Cfg {
  const out: Cfg = {};
  for (const [k, v] of Object.entries(flat)) {
    const parts = k.split('.');
    let o = out;
    for (const p of parts.slice(0, -1)) o = (o[p] ??= {}) as Cfg;
    o[parts[parts.length - 1]] = v;
  }
  return out;
}

export const fmtShutter = (s: number) => (s >= 1 ? `${s}` : `1/${Math.round(1 / s)}`);
export const parseShutter = (t: string) => {
  const m = t.trim().match(/^1\s*\/\s*(\d+(?:\.\d+)?)$/);
  return m ? 1 / Number(m[1]) : Number(t);
};
