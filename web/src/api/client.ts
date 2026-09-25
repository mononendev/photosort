// ---------------------------------------------------------------------------
// Types (mirror photosort/web/app.py)
// ---------------------------------------------------------------------------

export type ImageStatus = 'untracked' | 'pending' | 'analyzed' | 'tagged' | 'error';

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
  subject?: string;
  composition?: string;
  quality_score?: number | null;
  keeper?: boolean | null;
  overridden?: boolean;
  people_count?: number | null;
  description?: string | null;
  error?: string | null;
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
export interface TruthSummary {
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

export interface LocalResult {
  width: number; height: number; orientation: string; n_people: number; people: Person[];
  bg_sharp: number | null; global_sharp: number | null; primary_head_sharp: number | null; primary_body_sharp: number | null;
  primary_eye_sharp?: number | null; primary_eye_hf?: number | null; primary_eye_src?: string | null;
  crop_box: number[] | null; local_tier: number; local_reason: string; mask_boxes?: number[][];
  bg_terms?: MetricTerms | null; global_terms?: MetricTerms | null; eps?: number;
  exif?: { camera?: string; lens?: string; f_number?: number; shutter_s?: number; iso?: number; focal_mm?: number; focal_35mm?: number; taken?: string };
  af?: AfInfo | null;
  /** What picked people[0]: the camera's AF points, or prominence (size, centering, confidence) */
  primary_by?: 'af' | 'priority';
  exif_prior?: { dof_risk: string | null; motion_risk: string | null; shake_stops: number | null; pupil_mm?: number | null; summary: string | null };
}

export interface VlmResult {
  focus_tier: number; focus_notes: string; primary_subject: string; people_count: number; composition: string;
  subject_placement: string; action: string; keywords: string[]; adjectives: string[]; description: string;
  quality_remarks: string; quality_score: number; keeper: boolean;
}

export interface Override { focus_tier?: number; quality_score?: number; keeper?: boolean; note?: string }

export interface ImageDetail extends ImageSummary {
  local: LocalResult | null;
  vlm: VlmResult | null;
  override: Override | null;
  usage: Record<string, unknown> | null;
  final: Record<string, unknown>;
}

export interface TreeDir {
  name: string; path: string; images_direct: number; tracked: number; local_done: number; vlm_done: number; errors: number;
}
export interface Tree { path: string; dirs: TreeDir[]; files: ImageSummary[] }

export type JobState = 'queued' | 'running' | 'cancelling' | 'done' | 'cancelled' | 'failed';
export interface Job {
  id: number; created: number; started: number | null; finished: number | null;
  state: JobState; stage: string; paths: string[]; options: JobOptions;
  total: number; done: number; errors: number; message: string | null; rate?: number | null; eta_s?: number | null;
  /** Per-stage timings and settings, filled in as the job reaches each stage (empty for jobs from before this was recorded) */
  stages: Partial<Record<'scan' | 'local' | 'vlm', JobStage>>;
}
export interface JobStage {
  started: number; finished?: number; total?: number; done?: number; errors?: number; files?: number;
  workers?: number; device?: string | null; backend?: string; model?: string; concurrency?: number; base_url?: string | null;
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
  vlm?: boolean; skip_tier0?: boolean; rescan?: boolean; retry_errors?: boolean; concurrency?: number; model?: string | null;
}

export interface Stats {
  tracked: number; analyzed: number; tagged: number; errors: number; review: number; keepers: number;
  tiers: { tier0: number; tier1: number; tier2: number };
  lr_rated?: number;
  lr_by_tier?: { tier: number | null; rating: number; n: number }[];
}

export interface Health {
  ok: boolean; version: string; photos_root: string; workdir: string; device: string | null;
  backend: string; ollama: string | null; current_job: number | null;
}

export interface Calibration {
  metric: FocusMetric; keys: [string, string];  // [tier2 key, tier1 key] in config.focus
  count?: number; percentiles: Record<string, number>; thresholds?: Record<string, number | boolean>;
  samples: { id: number; sharp: number; tier: number }[];
}

export interface ImagesPage { total: number; offset: number; items: ImageSummary[] }

export interface ImageFilters {
  folder?: string; recursive?: boolean; tier?: number; keeper?: boolean; subject?: string; status?: string;
  review?: boolean; lr_rating?: number; lr_label?: string; truth_tier?: number; truth_mismatch?: boolean;
  q?: string; sort?: string; offset?: number; limit?: number;
}

export interface ExportRequest { name: string; folder?: string; link?: string; xmp?: boolean; focus_source?: string; tree?: boolean }
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
  image: (id: number) => request<ImageDetail>(`/api/images/${id}`),
  focusDebug: (id: number) => request<FocusDebug>(`/api/images/${id}/focus-debug`),
  override: (id: number, o: Override & { clear?: boolean }) =>
    request<ImageDetail>(`/api/images/${id}`, { method: 'PATCH', body: JSON.stringify(o) }),
  jobs: () => request<Job[]>('/api/jobs'),
  job: (id: number) => request<Job>(`/api/jobs/${id}`),
  createJob: (paths: string[], options: JobOptions) =>
    request<Job>('/api/jobs', { method: 'POST', body: JSON.stringify({ paths, ...options }) }),
  jobDetail: (id: number) => request<JobDetail>(`/api/jobs/${id}/detail`),
  jobItems: (id: number, f: { stage?: string; errors?: boolean; offset?: number; limit?: number }) =>
    request<JobItemsPage>(`/api/jobs/${id}/items${qs(f)}`),
  vlmRequest: (id: number, backend?: string, model?: string) =>
    request<VlmRequest>(`/api/images/${id}/vlm-request${qs({ backend, model })}`),
  cancelJob: (id: number) => request<Job>(`/api/jobs/${id}/cancel`, { method: 'POST' }),
  config: () => request<Record<string, unknown>>('/api/config'),
  putConfig: (values: Record<string, unknown>) =>
    request<Record<string, unknown>>('/api/config', { method: 'PUT', body: JSON.stringify({ values }) }),
  rescore: () => request<{ changed: number }>('/api/rescore', { method: 'POST' }),
  calibration: (n = 48, metric: FocusMetric = 'eye') => request<Calibration>(`/api/calibration${qs({ n, metric })}`),
  truth: () => request<TruthSummary>('/api/truth'),
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
};

export const thumbUrl = (id: number) => `/media/thumb/${id}`;
export const frameUrl = (id: number) => `/media/frame/${id}`;
export const cropUrl = (id: number) => `/media/crop/${id}`;
/** The original at native resolution (rendered on first request, then cached). */
export const fullUrl = (id: number) => `/media/full/${id}`;

export const TIER_LABEL: Record<number, string> = { 0: 'nobody in focus', 1: 'partly in focus', 2: 'sharp' };
export const TIER_CLASS: Record<number, string> = {
  0: 'bg-red-900/70 text-red-200 border-red-700',
  1: 'bg-amber-900/70 text-amber-200 border-amber-700',
  2: 'bg-emerald-900/70 text-emerald-200 border-emerald-700',
};
