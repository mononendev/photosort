/** Geometry and grading helpers for the detail overlay. Grading mirrors photosort/local.py (_grade). */
import type { Person } from '../api/client';
import { gradeBasis } from './explain';
import type { Cfg } from './explain';
import { focusThr } from './explain';

export const KP_NAMES = [
  'nose', 'left eye', 'right eye', 'left ear', 'right ear', 'left shoulder', 'right shoulder', 'left elbow', 'right elbow',
  'left wrist', 'right wrist', 'left hip', 'right hip', 'left knee', 'right knee', 'left ankle', 'right ankle',
];
/** COCO limb pairs, as YOLO pose draws them. */
export const SKELETON: [number, number][] = [
  [15, 13], [13, 11], [16, 14], [14, 12], [11, 12], [5, 11], [6, 12], [5, 6], [5, 7], [6, 8], [7, 9], [8, 10],
  [1, 2], [0, 1], [0, 2], [1, 3], [2, 4], [3, 5], [4, 6],
];
export const FACE_LM = ['right eye', 'left eye', 'nose tip', 'right mouth corner', 'left mouth corner'];
/** Keypoints below this confidence are ignored by the head/torso geometry (local.py pt()). */
export const KP_MIN_CONF = 0.3;
export const PERSON_COLORS = ['#22d3ee', '#c084fc', '#f472b6', '#fb923c', '#a3e635', '#60a5fa'];
export const GRADE_COLOR: Record<string, string> = { 2: '#34d399', 1: '#fbbf24', 0: '#f87171', none: '#9ca3af' };

export type Layer = 'people' | 'skeleton' | 'regions' | 'eyes' | 'crop' | 'mask' | 'heatmap';
export const LAYERS: { key: Layer; label: string; tip: string }[] = [
  { key: 'people', label: 'people', tip: 'Person boxes from YOLO pose, ranked by priority. #1 is the primary subject the tier is about.' },
  { key: 'skeleton', label: 'pose', tip: 'The 17 pose keypoints and limbs. Dots fade with confidence; hollow dots are below 0.3 and ignored by the head/torso geometry.' },
  { key: 'regions', label: 'head / torso', tip: 'The head box (solid: placed from keypoints; dashed: guessed from the top of the person box) and the shoulder-to-hip torso box that get their own sharpness numbers.' },
  { key: 'eyes', label: 'face / eyes', tip: 'The face model’s search window (dotted), its face box and 5 landmarks, and the eye band that decides the tier, colored by the grade it earned.' },
  { key: 'crop', label: 'model crop', tip: 'The native-resolution crop sent to the vision model alongside the frame.' },
  { key: 'mask', label: 'bg mask', tip: 'Everything outside these boxes is the “background” whose sharpness is compared against the subject.' },
  { key: 'heatmap', label: 'sharpness map', tip: 'The same contrast-normalized Laplacian metric computed per tile over the whole frame at native resolution (log scale). Shows where the lens actually focused. Loads from the original file.' },
];
export const DEFAULT_LAYERS: Layer[] = ['people', 'skeleton', 'regions', 'eyes'];

export type Grade = { grade: number | null; onEyes: boolean; checks: ReturnType<typeof gradeBasis>['checks'] };

/** 2/1/0 for one person, as local.py's _grade decides it (null when nothing is measurable). */
export function gradePerson(p: Person, cfg: Cfg): Grade {
  const { onEyes, checks } = gradeBasis(p, focusThr(cfg));
  if (checks.some((c) => c.value == null)) return { grade: null, onEyes, checks };
  const ok = (lvl: 't1' | 't2') => checks.every((c) => (c.value as number) >= c[lvl]);
  return { grade: ok('t2') ? 2 : ok('t1') ? 1 : 0, onEyes, checks };
}

export const boxW = (b: number[]) => b[2] - b[0];
export const boxH = (b: number[]) => b[3] - b[1];
