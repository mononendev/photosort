import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, cropUrl, frameUrl } from '../api/client';
import { TierBadge, Stars, LrBadge } from './TierBadge';

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[110px_1fr] gap-2 text-sm py-0.5">
      <span className="text-gray-500">{k}</span>
      <span className="text-gray-200 break-words">{v}</span>
    </div>
  );
}

export default function ImageDetail({ id, onClose, onNav }: { id: number; onClose: () => void; onNav?: (dir: 1 | -1) => void }) {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['image', id], queryFn: () => api.image(id) });
  const [note, setNote] = useState('');
  const ov = useMutation({
    mutationFn: (o: Parameters<typeof api.override>[1]) => api.override(id, o),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['image', id] });
      qc.invalidateQueries({ queryKey: ['images'] });
      qc.invalidateQueries({ queryKey: ['tree'] });
    },
  });
  const v = data?.vlm;
  const l = data?.local;
  const p = l?.people?.[0];
  return (
    <div className="fixed inset-0 z-50 flex" onKeyDown={(e) => { if (e.key === 'Escape') onClose(); if (e.key === 'ArrowRight') onNav?.(1); if (e.key === 'ArrowLeft') onNav?.(-1); }} tabIndex={-1}>
      <div className="absolute inset-0 bg-black/70" onClick={onClose} />
      <div className="relative m-auto w-[min(1200px,96vw)] max-h-[94vh] overflow-auto rounded-xl border border-gray-700 bg-gray-950 shadow-2xl">
        <div className="flex items-center gap-3 px-4 py-2 border-b border-gray-800 sticky top-0 bg-gray-950/95">
          <span className="font-mono text-sm text-gray-300 truncate">{data?.rel ?? id}</span>
          <TierBadge tier={data?.focus_tier} />
          <Stars n={data?.quality_score} />
          {data?.keeper ? <span className="text-xs text-emerald-300">keeper</span> : data?.keeper === false ? <span className="text-xs text-gray-500">cull</span> : null}
          {data?.overridden && <span className="text-xs text-purple-300">overridden</span>}
          <LrBadge rating={data?.lr_rating} label={data?.lr_label} />
          {data?.truth_tier !== null && data?.truth_tier !== undefined && <span className="text-xs text-gray-300">truth: tier {data.truth_tier}{data.truth_rating ? ` · ${data.truth_rating}★` : ''}{data.truth_label ? ` · ${data.truth_label}` : ''}</span>}
          <span className="ml-auto flex gap-2">
            {onNav && <button onClick={() => onNav(-1)} className="px-2 text-gray-400 hover:text-white">←</button>}
            {onNav && <button onClick={() => onNav(1)} className="px-2 text-gray-400 hover:text-white">→</button>}
            <button onClick={onClose} className="px-2 text-gray-400 hover:text-white">✕</button>
          </span>
        </div>
        <div className="grid md:grid-cols-[1fr_380px] gap-4 p-4">
          <div className="space-y-3">
            <img src={frameUrl(id)} alt="" className="w-full rounded-lg bg-gray-900 object-contain max-h-[60vh]" />
            {data?.has_crop && (
              <div className="flex gap-3 items-start">
                <img src={cropUrl(id)} alt="head crop" className="w-64 rounded-lg bg-gray-900" />
                <div className="text-xs text-gray-400 space-y-1">
                  <div>Native-resolution crop of the primary subject's head and upper body (what the model judges focus from).</div>
                  {p && (
                    <div className="font-mono text-gray-300">
                      {p.sharp_eye != null
                        ? <div>eyes {p.sharp_eye} · fft {p.hf_eye ?? '–'} <span className="text-gray-500">(via {p.eye_src === 'face' ? 'face landmarks' : 'pose keypoints'})</span></div>
                        : <div className="text-gray-500">eyes not located</div>}
                      head {p.sharp_head ?? '–'} · torso {p.sharp_torso ?? '–'} · body {p.sharp_body ?? '–'} · bg {l?.bg_sharp ?? '–'}
                      <div className="text-gray-500">head via {p.head_src} · {l?.n_people} people · local tier {l?.local_tier} ({l?.local_reason})</div>
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
          <div className="space-y-4">
            {v ? (
              <div>
                <Row k="model tier" v={<TierBadge tier={v.focus_tier} />} />
                <Row k="focus notes" v={v.focus_notes} />
                <Row k="subject" v={`${v.primary_subject} · ${v.composition} · ${v.subject_placement}`} />
                <Row k="action" v={v.action} />
                <Row k="people" v={v.people_count} />
                <Row k="description" v={v.description} />
                <Row k="keywords" v={<span className="flex flex-wrap gap-1">{v.keywords.map((k) => <span key={k} className="rounded bg-gray-800 px-1.5 text-xs">{k}</span>)}</span>} />
                <Row k="adjectives" v={<span className="flex flex-wrap gap-1">{v.adjectives.map((k) => <span key={k} className="rounded bg-gray-800/60 px-1.5 text-xs text-gray-300">{k}</span>)}</span>} />
                <Row k="remarks" v={v.quality_remarks} />
                <Row k="score" v={<><Stars n={v.quality_score} /> {v.keeper ? 'keeper' : 'cull'}</>} />
              </div>
            ) : (
              <p className="text-sm text-gray-500">{data?.status === 'analyzed' ? 'Not yet tagged by the vision model.' : data?.error ?? 'Not processed.'}</p>
            )}
            {l?.exif_prior?.summary && (
              <div className="text-xs text-gray-400">
                <span className="text-gray-500">camera: </span>{l.exif_prior.summary}
                {l.exif_prior.motion_risk === 'high' && <span className="ml-1 rounded bg-amber-900/60 px-1 text-amber-200">motion risk</span>}
                {l.exif_prior.dof_risk === 'high' && <span className="ml-1 rounded bg-sky-900/60 px-1 text-sky-200">shallow DOF</span>}
              </div>
            )}
            {data?.local && (
              <div className="text-xs text-gray-500">
                local: tier {data.local.local_tier} · {data.local.width}×{data.local.height} {data.local.orientation}
                {data.focus_tier_local !== null && data.focus_tier_vlm !== null && data.focus_tier_local !== data.focus_tier_vlm && (
                  <span className="text-amber-300"> · local ({data.focus_tier_local}) and model ({data.focus_tier_vlm}) disagree</span>
                )}
              </div>
            )}
            <div className="rounded-lg border border-gray-800 p-3 space-y-2">
              <div className="text-xs uppercase tracking-wide text-gray-500">Your call</div>
              <div className="flex gap-1 text-xs items-center">
                <span className="text-gray-500 w-14">focus</span>
                {[0, 1, 2].map((t) => (
                  <button key={t} onClick={() => ov.mutate({ focus_tier: t })} className={`px-2 py-1 rounded border ${data?.focus_tier === t ? 'border-blue-500 bg-blue-900/40' : 'border-gray-700 hover:border-gray-500'}`}>{t}</button>
                ))}
              </div>
              <div className="flex gap-1 text-xs items-center">
                <span className="text-gray-500 w-14">score</span>
                {[1, 2, 3, 4, 5].map((s) => (
                  <button key={s} onClick={() => ov.mutate({ quality_score: s })} className={`px-2 py-1 rounded border ${data?.quality_score === s ? 'border-amber-500 bg-amber-900/30' : 'border-gray-700 hover:border-gray-500'}`}>{s}</button>
                ))}
              </div>
              <div className="flex gap-1 text-xs items-center">
                <span className="text-gray-500 w-14">keep</span>
                <button onClick={() => ov.mutate({ keeper: true })} className={`px-2 py-1 rounded border ${data?.keeper === true ? 'border-emerald-500 bg-emerald-900/30' : 'border-gray-700'}`}>keeper</button>
                <button onClick={() => ov.mutate({ keeper: false })} className={`px-2 py-1 rounded border ${data?.keeper === false ? 'border-red-500 bg-red-900/30' : 'border-gray-700'}`}>cull</button>
                {data?.overridden && <button onClick={() => ov.mutate({ clear: true })} className="ml-auto text-gray-400 hover:text-white">reset</button>}
              </div>
              <div className="flex gap-1 text-xs">
                <input value={note} onChange={(e) => setNote(e.target.value)} placeholder={data?.override?.note ?? 'note'} className="flex-1 bg-gray-900 border border-gray-700 rounded px-2 py-1" />
                <button onClick={() => { ov.mutate({ note }); setNote(''); }} className="px-2 rounded border border-gray-700">save</button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
