import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, cropUrl, frameUrl } from '../api/client';
import { TierBadge, Stars, LrBadge } from './TierBadge';
import Tip from './Tip';
import { METRIC_TIPS, TIER_MEANING, explainDisagree, explainFinal, explainLocal, explainPrior } from '../lib/explain';

function Row({ k, v, tip }: { k: string; v: React.ReactNode; tip?: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[110px_1fr] gap-2 text-sm py-0.5">
      <span className="text-gray-500">{tip ? <Tip tip={tip}>{k}</Tip> : k}</span>
      <span className="text-gray-200 break-words">{v}</span>
    </div>
  );
}

export default function ImageDetail({ id, onClose, onNav }: { id: number; onClose: () => void; onNav?: (dir: 1 | -1) => void }) {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['image', id], queryFn: () => api.image(id) });
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config, staleTime: 30_000 });
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
          {data && <Tip plain tip={explainFinal(data, cfg)}><TierBadge tier={data.focus_tier} /></Tip>}
          <Tip plain tip={<>Quality score (1–5) and keep/cull verdict: {data?.override?.quality_score != null || data?.override?.keeper != null ? 'your call.' : "the vision model's opinion of the whole photo (exposure, framing, moment), not just focus."}</>}>
            <span className="inline-flex items-center gap-2">
              <Stars n={data?.quality_score} />
              {data?.keeper ? <span className="text-xs text-emerald-300">keeper</span> : data?.keeper === false ? <span className="text-xs text-gray-500">cull</span> : null}
            </span>
          </Tip>
          {data?.overridden && <Tip plain tip="You set at least one value under “Your call”. Your values beat the local and model results everywhere, including exports."><span className="text-xs text-purple-300">overridden</span></Tip>}
          <Tip plain tip={<>Rating and color label read from an XMP sidecar already next to the file (for example from Lightroom). Shown for reference only; it doesn't affect any tier. To calibrate against it, use <b>import from folder</b> on the Calibrate page.</>}>
            <LrBadge rating={data?.lr_rating} label={data?.lr_label} />
          </Tip>
          {data?.truth_tier !== null && data?.truth_tier !== undefined && (
            <Tip tip={<>Ground truth you imported on the Calibrate page. The tier comes from a focus:N keyword or CSV column first, then the color label, then the star rating (default: 4–5★ → 2, 2–3★ → 1, 1★ → 0). It's used to score both tiers and suggest thresholds.</>}>
              <span className="text-xs text-gray-300">truth: tier {data.truth_tier}{data.truth_rating ? ` · ${data.truth_rating}★` : ''}{data.truth_label ? ` · ${data.truth_label}` : ''}</span>
            </Tip>
          )}
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
                  <div>Native-resolution crop of the primary subject's head and upper body (what the model judges focus from). Hover any number for what it means.</div>
                  {p && l && (
                    <div className="font-mono text-gray-300 space-y-0.5">
                      {p.sharp_eye != null
                        ? <div><Tip tip={METRIC_TIPS.eyes}>eyes {p.sharp_eye}</Tip> · <Tip tip={METRIC_TIPS.fft}>fft {p.hf_eye ?? '–'}</Tip> <span className="text-gray-500">(via <Tip tip={METRIC_TIPS.eyeSrc[p.eye_src ?? 'pose']}>{p.eye_src === 'face' ? 'face landmarks' : 'pose keypoints'}</Tip>)</span></div>
                        : <div className="text-gray-500"><Tip tip={METRIC_TIPS.noEyes}>eyes not located</Tip></div>}
                      <div>
                        <Tip tip={METRIC_TIPS.head}>head {p.sharp_head ?? '–'}</Tip> · <Tip tip={METRIC_TIPS.torso}>torso {p.sharp_torso ?? '–'}</Tip> · <Tip tip={METRIC_TIPS.body}>body {p.sharp_body ?? '–'}</Tip> · <Tip tip={METRIC_TIPS.bg}>bg {l.bg_sharp ?? '–'}</Tip>
                      </div>
                      <div className="text-gray-500">
                        <Tip tip={METRIC_TIPS.headSrc[p.head_src]}>head via {p.head_src}</Tip> · <Tip tip={METRIC_TIPS.people}>{l.n_people} people</Tip> · <Tip tip={explainLocal(l, cfg)}>local tier {l.local_tier} ({l.local_reason})</Tip>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
          <div className="space-y-4">
            {v ? (
              <div>
                <Row k="model tier" v={<TierBadge tier={v.focus_tier} />} tip={<>The vision model's own focus verdict, from the downscaled frame plus the native-resolution crop, with the local numbers passed as evidence. Tier {v.focus_tier} means {TIER_MEANING[v.focus_tier]}.</>} />
                <Row k="focus notes" v={v.focus_notes} tip="The model's reason for its tier: where focus landed, and whether blur looks like missed focus (the whole subject soft while something else is crisp) or motion (a directional smear)." />
                <Row k="subject" v={`${v.primary_subject} · ${v.composition} · ${v.subject_placement}`} tip="Subject category · how much of the primary person is in frame · where they sit in the frame. From the model. The 4B model is shaky on subject labels for candid shots, so treat them as hints." />
                <Row k="action" v={v.action} />
                <Row k="people" v={v.people_count} />
                <Row k="description" v={v.description} />
                <Row k="keywords" v={<span className="flex flex-wrap gap-1">{v.keywords.map((k) => <span key={k} className="rounded bg-gray-800 px-1.5 text-xs">{k}</span>)}</span>} />
                <Row k="adjectives" v={<span className="flex flex-wrap gap-1">{v.adjectives.map((k) => <span key={k} className="rounded bg-gray-800/60 px-1.5 text-xs text-gray-300">{k}</span>)}</span>} />
                <Row k="remarks" v={v.quality_remarks} tip="Editor-style cull notes from the model: exposure, blur, noise, clipping, distractions, crop." />
                <Row k="score" v={<><Stars n={v.quality_score} /> {v.keeper ? 'keeper' : 'cull'}</>} tip="The model's overall quality (1–5) and whether a photographer would deliver it. Your call overrides both." />
              </div>
            ) : (
              <p className="text-sm text-gray-500">{data?.status === 'analyzed' ? 'Not yet tagged by the vision model.' : data?.error ?? 'Not processed.'}</p>
            )}
            {l?.exif_prior?.summary && (
              <div className="text-xs text-gray-400">
                <Tip tip="Read from the file's EXIF. The camera summary goes into the model's context. Motion risk can also demote a borderline local tier 2."><span className="text-gray-500">camera:</span></Tip> {l.exif_prior.summary}
                {l.exif_prior.motion_risk === 'high' && <Tip plain tip={explainPrior(l.exif_prior, cfg).motion}><span className="ml-1 rounded bg-amber-900/60 px-1 text-amber-200 cursor-help">motion risk</span></Tip>}
                {l.exif_prior.dof_risk === 'high' && <Tip plain tip={explainPrior(l.exif_prior, cfg).dof}><span className="ml-1 rounded bg-sky-900/60 px-1 text-sky-200 cursor-help">shallow DOF</span></Tip>}
              </div>
            )}
            {data?.local && (
              <div className="text-xs text-gray-500">
                <Tip tip={explainLocal(data.local, cfg)}>local: tier {data.local.local_tier}</Tip> · {data.local.width}×{data.local.height} {data.local.orientation}
                {data.focus_tier_local !== null && data.focus_tier_vlm != null && data.focus_tier_local !== data.focus_tier_vlm && (
                  <> · <Tip tip={explainDisagree(data.local, data.focus_tier_vlm, v?.focus_notes, cfg)} className="text-amber-300">local ({data.focus_tier_local}) and model ({data.focus_tier_vlm}) disagree</Tip></>
                )}
              </div>
            )}
            <div className="rounded-lg border border-gray-800 p-3 space-y-2">
              <div className="text-xs uppercase tracking-wide text-gray-500"><Tip tip="Your overrides. They beat the local and model results in the grid, the filters, and every export (tree, CSV, XMP). Reset clears them. They aren't used as calibration truth; import your exported ratings for that.">Your call</Tip></div>
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
