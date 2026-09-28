import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { PoseModel } from '../api/client';
import { errMsg } from '../lib/format';
import Tip from './Tip';

const FAMILY: Record<PoseModel['family'], string> = {
  yolo_nms: 'YOLO11 · NMS',
  yolo_e2e: 'YOLO26 · NMS-free',
  rtmo: 'RTMO · one-stage',
};

const mb = (b?: number) => (b ? `${Math.round(b / 1e6)} MB` : '');

/**
 * Which pose model finds the people. Changing it applies to photos analyzed from now on; the ones analyzed already
 * keep the people their model found until their local stage re-runs (the button queues that). The thresholds were
 * calibrated on one model's boxes, so check the agreement numbers below after switching.
 */
export default function PoseModelPanel() {
  const qc = useQueryClient();
  const { data: models } = useQuery({ queryKey: ['models'], queryFn: api.models, staleTime: 60_000 });
  const { data: stats } = useQuery({ queryKey: ['stats'], queryFn: api.stats });
  // Edits sit over the saved values until Save.
  const [draft, setDraft] = useState<{ model?: string; conf?: string; iou?: string }>({});
  const [msg, setMsg] = useState<string | null>(null);
  const cur = models?.current;
  const saved = { model: cur?.model.replace(/\.pt$/, '') ?? '', conf: String(cur?.conf ?? 0.25), iou: String(cur?.iou ?? 0.7) };
  const model = draft.model ?? saved.model, conf = draft.conf ?? saved.conf, iou = draft.iou ?? saved.iou;
  const setModel = (v: string) => setDraft((d) => ({ ...d, model: v }));
  const setConf = (v: string) => setDraft((d) => ({ ...d, conf: v }));
  const setIou = (v: string) => setDraft((d) => ({ ...d, iou: v }));
  const save = useMutation({
    mutationFn: () => api.putConfig({ detect_model: model, detect_conf: Number(conf), detect_iou: Number(iou) }, 'pose model'),
    onSuccess: () => {
      setDraft({});
      setMsg(`saved: new analyses use ${model}`);
      ['config', 'models', 'stats', 'config-history'].forEach((k) => qc.invalidateQueries({ queryKey: [k] }));
    },
    onError: (e) => setMsg(errMsg(e)),
  });
  const rerun = useMutation({
    mutationFn: () => api.createJob([''], { vlm: false, rescan: true, analyzed_only: true }),
    onSuccess: (j) => { setMsg(`queued job #${j.id}: re-analyzing every analyzed photo with ${model}`); qc.invalidateQueries({ queryKey: ['jobs'] }); },
    onError: (e) => setMsg(errMsg(e)),
  });
  const installed = models?.installed ?? [];
  const dirty = !!cur && (model !== saved.model || Number(conf) !== Number(saved.conf) || Number(iou) !== Number(saved.iou));
  const stale = stats?.detector_stale ?? 0;
  const field = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm';
  return (
    <section className="space-y-3 rounded-lg border border-gray-800 p-3 sm:p-4">
      <h2 className="font-semibold">Pose model</h2>
      <p className="text-sm text-gray-400 max-w-3xl">
        Finds the people, their head and eye keypoints, and so where focus is measured. A missed rider or a ghost box
        starts here. Try a model on a single photo first: open it and pick one under <i>compare detector</i>.
      </p>
      {installed.length === 0 ? (
        <p className="text-sm text-amber-300">The analyzer isn't answering, or has no models. <code>photosort models get yolo26s-pose</code> installs one.</p>
      ) : (
        <div className="flex flex-wrap items-end gap-3 text-sm">
          <label className="flex flex-col gap-1">
            <span className="text-xs text-gray-500">model</span>
            <select value={model} onChange={(e) => setModel(e.target.value)} className={field}>
              {installed.map((m) => (
                <option key={m.name} value={m.name}>{m.name} · {FAMILY[m.family]} · {m.imgsz}px {mb(m.bytes) && `· ${mb(m.bytes)}`}</option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1">
            <Tip tip="Detections below this confidence are dropped. Lower finds more people who are small, blurred or partly hidden, and more ghosts."><span className="text-xs text-gray-500">min confidence</span></Tip>
            <input type="number" step="0.05" min="0.05" max="0.95" value={conf} onChange={(e) => setConf(e.target.value)} className={`${field} w-24`} />
          </label>
          <label className="flex flex-col gap-1">
            <Tip tip="YOLO11 only: two boxes overlapping more than this are one person. YOLO26 and RTMO resolve overlaps inside the model."><span className="text-xs text-gray-500">NMS overlap</span></Tip>
            <input type="number" step="0.05" min="0.3" max="0.95" value={iou} onChange={(e) => setIou(e.target.value)} className={`${field} w-24`} />
          </label>
          <button disabled={!dirty || save.isPending} onClick={() => save.mutate()}
            className="px-3 py-1.5 rounded-md bg-blue-600 hover:bg-blue-500 disabled:opacity-40 text-sm">Save</button>
          <span className="text-xs text-gray-500">runs on {models?.device ?? '?'}</span>
        </div>
      )}
      {stale > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-amber-300">
            <Link to="/photos?stale_detector=1" className="hover:underline">{stale} analyzed photo{stale === 1 ? '' : 's'}</Link> still have people found by another model.
          </span>
          <button disabled={rerun.isPending || dirty} onClick={() => rerun.mutate()} title={dirty ? 'Save first' : undefined}
            className="px-2 py-1 rounded border border-gray-700 hover:border-gray-500 disabled:opacity-40 text-xs">Re-analyze all analyzed photos (local stage only)</button>
        </div>
      )}
      {msg && <p className="text-xs text-gray-400">{msg}</p>}
    </section>
  );
}
