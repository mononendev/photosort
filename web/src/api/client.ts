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
export interface TruthSummary {
  images_with_truth: number; with_tier: number;
  local: { matrix: TruthMatrixRow[]; accuracy: number | null };
  vlm: { matrix: TruthMatrixRow[]; accuracy: number | null };
  suggested: { tier2_min?: { value: number; balanced_accuracy: number }; tier1_min?: { value: number; balanced_accuracy: number } };
  mapping: { label_tiers: Record<string, number>; rating_tiers: Record<string, number | null> } | null;
}
export interface TruthImportResult { verdicts: number; matched: number; unmatched: number; summary: TruthSummary }

export interface Person {
  box: number[]; head: number[]; head_src: string; torso: number[]; upper: number[]; conf: number;
  area_frac: number; center: number[]; center_dist: number;
  sharp_head: number | null; sharp_torso: number | null; sharp_body: number | null;
}

export interface LocalResult {
  width: number; height: number; orientation: string; n_people: number; people: Person[];
  bg_sharp: number | null; global_sharp: number | null; primary_head_sharp: number | null; primary_body_sharp: number | null;
  crop_box: number[] | null; local_tier: number; local_reason: string;
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
  count?: number; percentiles: Record<string, number>; thresholds?: { tier1_min: number; tier2_min: number };
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
  override: (id: number, o: Override & { clear?: boolean }) =>
    request<ImageDetail>(`/api/images/${id}`, { method: 'PATCH', body: JSON.stringify(o) }),
  jobs: () => request<Job[]>('/api/jobs'),
  job: (id: number) => request<Job>(`/api/jobs/${id}`),
  createJob: (paths: string[], options: JobOptions) =>
    request<Job>('/api/jobs', { method: 'POST', body: JSON.stringify({ paths, ...options }) }),
  cancelJob: (id: number) => request<Job>(`/api/jobs/${id}/cancel`, { method: 'POST' }),
  config: () => request<Record<string, unknown>>('/api/config'),
  putConfig: (values: Record<string, unknown>) =>
    request<Record<string, unknown>>('/api/config', { method: 'PUT', body: JSON.stringify({ values }) }),
  rescore: () => request<{ changed: number }>('/api/rescore', { method: 'POST' }),
  calibration: (n = 48) => request<Calibration>(`/api/calibration${qs({ n })}`),
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

export const TIER_LABEL: Record<number, string> = { 0: 'nobody in focus', 1: 'partly in focus', 2: 'sharp' };
export const TIER_CLASS: Record<number, string> = {
  0: 'bg-red-900/70 text-red-200 border-red-700',
  1: 'bg-amber-900/70 text-amber-200 border-amber-700',
  2: 'bg-emerald-900/70 text-emerald-200 border-emerald-700',
};
