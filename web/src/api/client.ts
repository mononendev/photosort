// ---------------------------------------------------------------------------
// Types (mirror photosort/web/app.py)
// ---------------------------------------------------------------------------

export type ImageStatus = 'untracked' | 'pending' | 'analyzed' | 'skipped' | 'tagged' | 'error';

export interface ImageSummary {
  id: number | null;
  path: string;
  rel: string;
  name: string;
  folder?: string;
  status: ImageStatus;
  has_crop?: boolean;
  focus_tier?: number | null;
  focus_tier_local?: number | null;
  focus_tier_vlm?: number | null;
  review?: boolean;
  split?: MetricSplit | null;  // the primary's metrics disagree among themselves (local.metric_split)
  subject?: string;
  composition?: string;
  quality_score?: number | null;
  keeper?: boolean | null;
  overridden?: boolean;
  rating?: number | null;      // your cull: 0-3 focus tier, 4 banger (see RATINGS)
  reviewed?: boolean;
  group?: number | null;       // your sort group 1-4 (see GROUPS)
  people_count?: number | null;
  description?: string | null;
  error?: string | null;
  vlm_skip?: string | null;    // why a job left it for the vision model (status 'skipped')
  lr_rating?: number | null;   // rating from an existing sidecar next to the file (informational)
  lr_label?: string | null;
  truth_tier?: number | null;  // your exported ground truth for calibration
  truth_rating?: number | null;
  truth_label?: string | null;
}

export interface TruthMatrixRow { truth: number; pred: number; n: number }
/** head = head-box Laplacian (fallback), eye = eye-band Laplacian, hf = eye-band FFT ratio */
export type FocusMetric = 'head' | 'eye' | 'hf';
export const FOCUS_METRIC_LABEL: Record<FocusMetric, string> = { eye: 'eye band · Laplacian', hf: 'eye band · FFT detail ratio', head: 'head box · Laplacian (no eyes found)' };
/** Where calibration truth comes from: your in-app ratings, imported verdicts, or both (your rating wins). */
export type TruthSource = 'both' | 'ratings' | 'imported';
export interface TruthSummary {
  source: TruthSource; rated: number; imported: number;
  images_with_truth: number; with_tier: number;
  local: { matrix: TruthMatrixRow[]; accuracy: number | null };
  vlm: { matrix: TruthMatrixRow[]; accuracy: number | null };
  suggested: Partial<Record<FocusMetric, { n: number } & Partial<Record<string, { value: number; balanced_accuracy: number }>>>>;
  mapping: { label_tiers: Record<string, number>; rating_tiers: Record<string, number | null> } | null;
}
export interface TruthImportResult { verdicts: number; matched: number; unmatched: number; summary: TruthSummary }

export interface Person {
  box: number[]; head: number[]; head_src: string; torso: number[]; upper: number[]; conf: number;
  area_frac: number; center: number[]; center_dist: number;
  sharp_head: number | null; sharp_torso: number | null; sharp_body: number | null;
  eyes?: number[][] | null; eye_src?: 'face' | 'pose' | null; eye?: number[] | null;
  sharp_eye?: number | null; hf_eye?: number | null;
  /** COCO-17 pose keypoints [x, y, confidence] in full-res pixels (images analyzed after the overlay landed) */
  kp?: number[][] | null;
  /** YuNet face: box, 5 landmarks (right eye, left eye, nose, right/left mouth corner), the window it searched */
  face?: { box: number[]; search: number[]; score: number; lm: number[][] } | null;
  priority?: number;
  /** How strongly the camera's active AF points land on this person (head hit 2, torso 1.5, body 1 per point) */
  af_score?: number | null;
  /** Each metric's stored terms: value = lap_var / (gray_var + eps); eye FFT ratio = band_e / total_e */
  terms?: Partial<Record<'head' | 'torso' | 'body' | 'eye', MetricTerms | null>>;
  /** Edge-width blur (px) of the head, torso and sharpest surroundings, and the extra blur between them (primary only) */
  plane?: FocusPlane | null;
}

export interface FocusPlane {
  head: number; torso: number | null; near: number | null;
  head_vs_near: number | null; head_vs_torso: number | null; torso_vs_near: number | null;
  n_torso: number; n_near: number; tile: number;
}

export interface MetricTerms { lap_var: number; gray_var: number; px?: number[]; px_count?: number; band_e?: number; total_e?: number }

export interface FocusView { img: string; lap_var: number; gray_var: number; eps: number; value: number; size: number[] }
export interface SpectrumView {
  img: string; size: number[]; band: number[]; floor: number; value: number | null;
  profile: { edges: number[]; energy: number[] }; band_energy: number; total_energy: number;
}
export interface FocusDebug {
  width: number; height: number;
  heatmap: { img: string; tile: number; grid: number[]; cover: number[]; log_range: number[] };
  people: { eye?: { box: number[]; img: string; laplacian: FocusView | null; spectrum: SpectrumView | null }; head?: { box: number[]; img: string; laplacian: FocusView | null } }[];
}

/** Camera AF points placed on the upright frame (full-res pixels). Only selected / in-focus points are stored. */
export interface AfInfo {
  source: string; mode: number; mode_name: string; user_placed: boolean; n_points: number; primary_point: number | null;
  points: { i: number; box: number[]; in_focus: boolean; selected: boolean }[];
  active: number[]; active_from: 'in_focus' | 'selected' | null;
}

/** One line of config_history.jsonl: a save (with the whole config from just before it) or a re-score. */
export type ConfigHistoryEntry = { id: number; at: string; source: string } & (
  | { kind: 'change'; changes: { key: string; from: unknown; to: unknown }[]; before: Record<string, unknown> }
  | { kind: 'rescore'; changed: number });

export interface RescoreResult {
  changed: number; exif_backfilled: number; af_backfilled: number; primary_changed: number;
  errors: number; first_error: string | null;
}

/** local.metric_split(): each metric's own tier; `odd` sits `gap` tiers from the nearest other. */
export interface MetricSplit { grades: Partial<Record<'eye' | 'fft' | 'head', number>>; odd: 'eye' | 'fft' | 'head'; gap: number }

export interface LocalResult {
  width: number; height: number; orientation: string; n_people: number; people: Person[];
  bg_sharp: number | null; global_sharp: number | null; primary_head_sharp: number | null; primary_body_sharp: number | null;
  primary_eye_sharp?: number | null; primary_eye_hf?: number | null; primary_eye_src?: string | null;
  crop_box: number[] | null; local_tier: number; local_reason: string; split?: MetricSplit | null; mask_boxes?: number[][];
  bg_terms?: MetricTerms | null; global_terms?: MetricTerms | null; eps?: number;
  exif?: { camera?: string; lens?: string; f_number?: number; shutter_s?: number; iso?: number; focal_mm?: number; focal_35mm?: number; taken?: string };
  af?: AfInfo | null;
  /** What the AF read found, e.g. "spot, 1 active" or why there is nothing */
  af_note?: string | null;
  /** What picked people[0]: the camera's AF points, or prominence (size, centering, confidence) */
  primary_by?: 'af' | 'priority';
  /** The pose model that found the people (absent on rows from before it was recorded: yolo11n-pose) */
  detector?: string;
  /** Set when an underexposed frame was brightened before analysis: stops added, what was lifted, and the scene key */
  exposure?: { ev: number; source: 'raw' | 'jpeg'; key: number; p99: number } | null;
  exif_prior?: { dof_risk: string | null; motion_risk: string | null; shake_stops: number | null; pupil_mm?: number | null; summary: string | null };
  /** Noise risk: from the measured noise sigma (8-bit levels) when its cuts are set, else effective ISO (ISO × 2^lift) */
  noise?: NoisePrior | null;
}

export interface NoisePrior {
  iso: number | null; ev: number | null; eff_iso: number | null; sigma: number | null;
  risk: 'low' | 'medium' | 'high' | null; by: 'measured' | 'iso' | null; summary: string | null;
}

export interface VlmResult {
  focus_tier: number; focus_notes: string; primary_subject: string; people_count: number; composition: string;
  subject_placement: string; action: string; keywords: string[]; adjectives: string[]; description: string;
  quality_remarks: string; quality_score: number; keeper: boolean;
}

export interface Override { rating?: number; focus_tier?: number; quality_score?: number; group?: number; keeper?: boolean; note?: string }

export interface ImageDetail extends ImageSummary {
  local: LocalResult | null;
  vlm: VlmResult | null;
  /** The model's verdict was made on a frame with a different exposure lift than the local stage now has */
  vlm_stale?: boolean;
  override: Override | null;
  usage: Record<string, unknown> | null;
  final: Record<string, unknown>;
}

export interface TreeDir {
  name: string; path: string; images_direct: number; tracked: number; local_done: number; vlm_done: number; errors: number;
}
/** GET /api/images/{id}/trace (photosort/trace.py): every rule the pipeline applies to one photo, in order. */
export interface TraceKV { k: string; v: unknown; note?: string }
export interface TracePerson { n: number; ok: boolean; tests?: Record<string, boolean>; conf?: number | null; grade?: number | null }
export interface TraceNode {
  q: string;
  /** true: the rule holds (fires); false: it doesn't; null: off, or nothing to read */
  result: boolean | null;
  rule: string | null; inputs: TraceKV[];
  /** What firing does (or would have done, when not reached) */
  effect: string | null; note: string | null;
  /** false when an earlier rule already decided, or its branch wasn't taken; it is evaluated anyway */
  reached: boolean;
  /** the one rule that settled the stage */
  decided: boolean;
  people: TracePerson[] | null;
}
export type TraceTable =
  | { kind: 'people'; rows: { n: number; conf: number; area: number; center: number; priority: number | null; head_src: string }[] }
  | { kind: 'scores'; rows: { n: number; af_score: number | null; priority: number | null }[] }
  | { kind: 'grade'; tiers: number[]; rows: { label: string; value: number | null; cuts: (number | null)[]; ok: boolean[] }[] }
  | { kind: 'paths'; rows: string[] };
export interface TraceStage {
  key: string; title: string;
  state: 'done' | 'skipped' | 'pending' | 'error' | 'off';
  summary: string; facts: TraceKV[]; nodes: TraceNode[]; table: TraceTable | null;
  outcome: { label: string; tier?: number | null; reason?: string; person?: number } | null;
}
export interface Trace {
  id: number; rel: string; focus_source: string; stages: TraceStage[];
  /** local tier as this walk reaches it, as local.local_tier gives it now, and as stored at the last (re)score */
  check: { traced?: [number | null, string | null]; engine?: [number, string]; stored?: [number | null, string | null] };
  primary: number | null;
  final: { tier: number | null; local: number | null; vlm: number | null; review: boolean; rating: number | null };
}

export interface Tree { path: string; dirs: TreeDir[]; files: ImageSummary[] }

export type JobState = 'queued' | 'running' | 'preempting' | 'cancelling' | 'done' | 'cancelled' | 'failed';
export interface Job {
  id: number; created: number; started: number | null; finished: number | null;
  state: JobState; stage: string; paths: string[]; options: JobOptions;
  total: number; done: number; errors: number; message: string | null; rate?: number | null; eta_s?: number | null;
  /** While running: main runs the whole job; ahead runs its local stage while the main job waits on the vision model */
  lane: 'main' | 'ahead' | null; priority: number;
  /** Per-stage timings and settings, filled in as the job reaches each stage (empty for jobs from before this was recorded) */
  stages: Partial<Record<'scan' | 'local' | 'vlm', JobStage>>;
}
export interface JobStage {
  started: number; finished?: number; total?: number; done?: number; errors?: number; files?: number;
  workers?: number; device?: string | null; backend?: string; model?: string; concurrency?: number; base_url?: string | null;
  skipped?: number;            // vlm: images left out at local tier 0 (skip nobody-in-focus)
}
export interface VlmUsage {
  in?: number | null; out?: number | null; seconds?: number; model?: string;
  prefill_s?: number; decode_s?: number; tok_s?: number | null;
}
export interface StageStats {
  n: number; errors: number; avg_s: number; p50_s: number; p95_s: number; max_s: number;
  rate: number | null; recent_rate: number | null;
  tokens_in: number; tokens_out: number; tok_s: number | null; recent_tok_s: number | null;
  avg_in: number | null; avg_out: number | null; avg_prefill_s: number | null; avg_decode_s: number | null;
}
export interface ActiveItem { id: number; path: string; name: string; rel: string; stage: string; started: number; elapsed: number; has_thumb: boolean }
export interface JobDetail extends Job {
  stats: Partial<Record<'local' | 'vlm', StageStats>>;
  series: { t: number; stage: string; s: number; err: boolean; tok_s: number | null; out: number | null }[];
  active: ActiveItem[];
  now: number;   // server clock, for elapsed times
  runner: { device: string | null; backend: string; model: string | null; base_url: string | null; workers: number; vlm_concurrency: number };
}
export interface JobItem {
  id: number; image_id: number; stage: 'local' | 'vlm'; started: number; finished: number; seconds: number;
  error: string | null; usage: VlmUsage | null; name: string | null; rel: string | null; has_crop: boolean;
  local?: { local_tier: number; local_reason: string; n_people: number; primary_eye_sharp: number | null;
    primary_eye_hf: number | null; primary_head_sharp: number | null; primary_by?: string };
  vlm?: { focus_tier: number; primary_subject: string; composition: string; quality_score: number; keeper: boolean;
    description: string; keywords: string[] };
}
export interface JobItemsPage { total: number; offset: number; items: JobItem[] }
export interface VlmRequest {
  backend: string; model: string; system: string; context: string;
  images: { label: string; url: string; bytes: number }[];
  request: Record<string, unknown> | null; build_error: string | null;
}
export interface JobOptions {
  vlm?: boolean; skip_tier0?: boolean; rescan?: boolean; revlm?: boolean; retry_errors?: boolean; concurrency?: number; model?: string | null;
  /** Only redo the local stage on images analyzed already: no scan, no new files registered */
  analyzed_only?: boolean;
  /** Pose model for this job's local stage, instead of the configured one */
  detector?: { model: string } | null;
}

export interface Stats {
  tracked: number; analyzed: number; tagged: number; errors: number; review: number; keepers: number;
  tiers: { tier0: number; tier1: number; tier2: number; tier3: number };
  lr_rated?: number;
  lr_by_tier?: { tier: number | null; rating: number; n: number }[];
  /** The configured pose model, and how many analyzed photos another model found the people in */
  detector?: string; detector_stale?: number;
}

export interface Health {
  ok: boolean; version: string; photos_root: string; workdir: string; device: string | null;
  backend: string; ollama: string | null; current_job: number | null;
  database?: 'sqlite' | 'postgres'; analyzer?: boolean;
}

/** A pose model the analyzer can run (GET /api/models). */
export interface PoseModel { name: string; family: 'yolo_nms' | 'yolo_e2e' | 'rtmo'; imgsz: number; license?: string; bytes?: number }
export interface ModelsInfo {
  installed: PoseModel[];
  current: { model: string; imgsz?: number; conf?: number; iou?: number };
  device: string | null;
}

/** Another pose model's detections on one photo, stored nowhere (POST /api/images/{id}/detect). */
export interface DetectResult {
  width: number; height: number; model: string; seconds: number; raw: number;
  people: { box: number[]; conf: number; kp: [number, number, number][] | null }[];
}

export interface Calibration {
  metric: FocusMetric; keys: [string, string, string];  // [tier3, tier2, tier1] keys in config.focus
  count?: number; percentiles: Record<string, number>; thresholds?: Record<string, number | boolean>;
  quantiles?: number[];   // 101 values: quantiles[i] is the score i% of photos are at or below
  samples: { id: number; sharp: number; tier: number }[];
}

export interface ImagesPage { total: number; offset: number; items: ImageSummary[] }

export interface ImageFilters {
  folder?: string; recursive?: boolean; tier?: number; keeper?: boolean; subject?: string; status?: string;
  review?: boolean; split?: boolean; lr_rating?: number; lr_label?: string; truth_tier?: number; truth_mismatch?: boolean;
  rating?: number; reviewed?: boolean; group?: number;   // group 0: in none
  local_tier?: number; vlm_tier?: number; stages?: 'agree' | 'disagree'; stale?: boolean; composition?: string;
  eye_src?: 'face' | 'pose' | 'none'; primary_by?: 'af' | 'priority'; lifted?: boolean; overridden?: boolean; noted?: boolean;
  camera?: string; lens?: string;
  people_min?: number; people_max?: number; score_min?: number; score_max?: number; eye_min?: number; eye_max?: number;
  iso_min?: number; iso_max?: number; f_min?: number; f_max?: number; shutter_min?: number; shutter_max?: number;
  focal_min?: number; focal_max?: number; taken_from?: string; taken_to?: string;
  detector?: string; stale_detector?: boolean;
  q?: string; sort?: string; offset?: number; limit?: number;
}

/** What the image filters can pick from in this library (GET /api/images/facets). */
export interface ImageFacets {
  cameras: { value: string; n: number }[]; lenses: { value: string; n: number }[];
  compositions: { value: string; n: number }[]; subjects: { value: string; n: number }[]; lr_labels: { value: string; n: number }[];
  /** [min, max] over the library, null when no photo has the value; taken is an EXIF date string */
  ranges: Record<'iso' | 'f' | 'shutter' | 'focal' | 'people' | 'score' | 'eye', [number | null, number | null]> & { taken: [string | null, string | null] };
}

export interface ExportRequest { name: string; folder?: string; link?: string; xmp?: boolean; focus_source?: string; tree?: boolean }
export type XMPFormat = 'capture_one' | 'lightroom';
export interface ExportResult { out: string; images: number; tree: Record<string, number>; xmp_written: number }

// ---------------------------------------------------------------------------
// Fetch helpers
// ---------------------------------------------------------------------------

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const body = await res.json();
      msg = typeof body.detail === 'string' ? body.detail : JSON.stringify(body.detail ?? body);
    } catch { /* ignore */ }
    throw new ApiError(res.status, msg);
  }
  return res.json() as Promise<T>;
}

function qs(params: Record<string, unknown>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue;
    p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}

export const api = {
  health: () => request<Health>('/api/health'),
  stats: () => request<Stats>('/api/stats'),
  tree: (path: string) => request<Tree>(`/api/tree${qs({ path })}`),
  images: (f: ImageFilters) => request<ImagesPage>(`/api/images${qs(f as Record<string, unknown>)}`),
  imageFacets: () => request<ImageFacets>('/api/images/facets'),
  /** Forget the images matching the filters (and under `paths`, if given); rated, edited and ground-truth photos stay.
   * `dry` only counts. The files on disk are untouched. */
  untrack: (f: ImageFilters, paths?: string[], dry = false) =>
    request<{ untracked: number; kept: number }>(`/api/images/untrack${qs({ ...f, offset: undefined, limit: undefined, sort: undefined, dry_run: dry || undefined })}`,
      { method: 'POST', body: paths ? JSON.stringify({ paths }) : undefined }),
  image: (id: number) => request<ImageDetail>(`/api/images/${id}`),
  trace: (id: number) => request<Trace>(`/api/images/${id}/trace`),
  focusDebug: (id: number) => request<FocusDebug>(`/api/images/${id}/focus-debug`),
  override: (id: number, o: Override & { clear?: boolean; clear_rating?: boolean; clear_score?: boolean; clear_group?: boolean }) =>
    request<ImageDetail>(`/api/images/${id}`, { method: 'PATCH', body: JSON.stringify(o) }),
  jobs: () => request<Job[]>('/api/jobs'),
  createJob: (paths: string[], options: JobOptions) =>
    request<Job>('/api/jobs', { method: 'POST', body: JSON.stringify({ paths, ...options }) }),
  jobDetail: (id: number) => request<JobDetail>(`/api/jobs/${id}/detail`),
  jobItems: (id: number, f: { stage?: string; errors?: boolean; offset?: number; limit?: number }) =>
    request<JobItemsPage>(`/api/jobs/${id}/items${qs(f)}`),
  vlmRequest: (id: number, backend?: string, model?: string) =>
    request<VlmRequest>(`/api/images/${id}/vlm-request${qs({ backend, model })}`),
  cancelJob: (id: number) => request<Job>(`/api/jobs/${id}/cancel`, { method: 'POST' }),
  overrideJob: (id: number) => request<Job>(`/api/jobs/${id}/override`, { method: 'POST' }),
  config: () => request<Record<string, unknown>>('/api/config'),
  configDefaults: () => request<Record<string, unknown>>('/api/config/defaults'),
  /** `source` names the save in the change history (Calibrate → history). */
  putConfig: (values: Record<string, unknown>, source = 'edit') =>
    request<Record<string, unknown>>('/api/config', { method: 'PUT', body: JSON.stringify({ values, source }) }),
  rescore: (source = '') => request<RescoreResult>('/api/rescore', { method: 'POST', body: JSON.stringify({ source }) }),
  configHistory: () => request<ConfigHistoryEntry[]>('/api/config/history'),
  configRestore: (id: number) => request<Record<string, unknown>>(`/api/config/history/${id}/restore`, { method: 'POST' }),
  calibration: (n = 48, metric: FocusMetric = 'eye') => request<Calibration>(`/api/calibration${qs({ n, metric })}`),
  truth: (source: TruthSource = 'both') => request<TruthSummary>(`/api/truth?source=${source}`),
  truthClear: () => request<{ cleared: number }>('/api/truth', { method: 'DELETE' }),
  truthImport: (dir: string, folder = '') =>
    request<TruthImportResult>('/api/truth/import', { method: 'POST', body: JSON.stringify({ dir, folder }) }),
  truthUpload: async (files: FileList, folder = ''): Promise<TruthImportResult> => {
    const fd = new FormData();
    Array.from(files).forEach((f) => fd.append('files', f));
    const res = await fetch(`/api/truth/upload${folder ? `?folder=${encodeURIComponent(folder)}` : ''}`, { method: 'POST', body: fd });
    if (!res.ok) throw new ApiError(res.status, await res.text());
    return res.json();
  },
  exportRun: (e: ExportRequest) => request<ExportResult>('/api/export', { method: 'POST', body: JSON.stringify(e) }),
  exports: () => request<{ name: string; path: string; mtime: number }[]>('/api/exports'),
  /** The XMP sidecars under folder as a zip laid out like the photos there; saved by the browser. */
  xmpZip: async (p: { folder?: string; format: XMPFormat; focus_source?: string }) => {
    const res = await fetch(`/api/export/xmp.zip${qs(p)}`);
    if (!res.ok) {
      let msg = res.statusText;
      try { msg = (await res.json()).detail ?? msg; } catch { /* ignore */ }
      throw new ApiError(res.status, msg);
    }
    const name = /filename="?([^";]+)"?/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'xmp.zip';
    const url = URL.createObjectURL(await res.blob());
    const a = Object.assign(document.createElement('a'), { href: url, download: name });
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    return name;
  },
  models: () => request<ModelsInfo>('/api/models'),
  detect: (id: number, model: string) => request<DetectResult>(`/api/images/${id}/detect${qs({ model })}`, { method: 'POST' }),
};

export const thumbUrl = (id: number) => `/media/thumb/${id}`;
export const frameUrl = (id: number) => `/media/frame/${id}`;
export const cropUrl = (id: number) => `/media/crop/${id}`;
/** The original at native resolution (rendered on first request, then cached). */
export const fullUrl = (id: number) => `/media/full/${id}`;
/** A TILE_SIZE-square piece of the full-resolution render, downscaled 2^z (z = 0 is native); see internal/api/tiles.go. */
export const tileUrl = (id: number, z: number, x: number, y: number) => `/media/tile/${id}?z=${z}&x=${x}&y=${y}`;
export const TILE_SIZE = 512;

/** Your cull ratings, in key order (q w e r t). 0-3 are the focus tiers; 4 is a banger, which only you give.
 * Exported as these color labels. */
export const RATINGS = [
  { value: 0, key: 'q', short: '0', label: 'missed', color: 'Red', hex: '#f87171', cls: 'bg-red-900/70 text-red-200 border-red-700', solid: 'bg-red-600 border-red-400' },
  { value: 1, key: 'w', short: '1', label: 'soft', color: 'Orange', hex: '#fb923c', cls: 'bg-orange-900/70 text-orange-200 border-orange-700', solid: 'bg-orange-500 border-orange-300 text-gray-950' },
  { value: 2, key: 'e', short: '2', label: 'slightly soft', color: 'Yellow', hex: '#facc15', cls: 'bg-yellow-900/70 text-yellow-200 border-yellow-700', solid: 'bg-yellow-400 border-yellow-200 text-gray-950' },
  { value: 3, key: 'r', short: '3', label: 'sharp', color: 'Green', hex: '#34d399', cls: 'bg-emerald-900/70 text-emerald-200 border-emerald-700', solid: 'bg-emerald-600 border-emerald-400' },
  { value: 4, key: 't', short: '★', label: 'banger', color: 'Blue', hex: '#60a5fa', cls: 'bg-blue-900/70 text-blue-200 border-blue-600', solid: 'bg-blue-600 border-blue-400' },
] as const;
export const BANGER = 4;
/** Your sort groups, in key order (a s d f). Each exports to its own folder with its keywords (config "groups"). */
export const GROUPS = [
  { value: 1, key: 'a' }, { value: 2, key: 's' }, { value: 3, key: 'd' }, { value: 4, key: 'f' },
] as const;
/** Config "groups": per group, the export folder and the XMP keywords. */
export type GroupsConfig = Record<string, { folder: string; keywords: string[] }>;
/** The focus tiers, worst to best. */
export const SUBJECTS = ['rider_action', 'rider_posed', 'group', 'crowd_spectators', 'gear_board', 'venue_scenery', 'other', 'no_people'];
export const TIERS = [0, 1, 2, 3] as const;
export const COMPOSITIONS = ['full_body', 'three_quarter', 'half_body', 'close_up', 'environmental', 'no_subject'];

/** Focus tiers 0-3 share their label and colors with the matching cull rating. */
export const TIER_LABEL: Record<number, string> = Object.fromEntries(RATINGS.slice(0, BANGER).map((r) => [r.value, r.label]));
export const TIER_CLASS: Record<number, string> = Object.fromEntries(RATINGS.slice(0, BANGER).map((r) => [r.value, r.cls]));
/** Text/stroke color per tier (or per-person grade), for SVG and inline styles; `none` when nothing was measurable. */
export const TIER_COLOR: Record<string, string> = { ...Object.fromEntries(RATINGS.slice(0, BANGER).map((r) => [r.value, r.hex])), none: '#9ca3af' };

/** A job a worker holds right now. */
export const isLive = (j: Pick<Job, 'state'> | undefined) => !!j && (j.state === 'running' || j.state === 'preempting' || j.state === 'cancelling');
/** A job that will still change: live or waiting in the queue. */
export const isBusy = (j: Pick<Job, 'state'>) => isLive(j) || j.state === 'queued';
export const isFinished = (j: Pick<Job, 'state'>) => j.state === 'done' || j.state === 'cancelled' || j.state === 'failed';
