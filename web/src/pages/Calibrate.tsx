import { useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, cropUrl, FOCUS_METRIC_LABEL, TIER_COLOR, TIERS } from '../api/client';
import type { FocusMetric, RescoreResult, TruthMatrixRow, TruthSummary } from '../api/client';
import SegButton from '../components/SegButton';
import Tip from '../components/Tip';
import { errMsg } from '../lib/format';

const METRIC_TAB_TIP: Record<FocusMetric, string> = {
  eye: 'Laplacian on the band across both eyes. Decides the tier (together with the FFT ratio) whenever the eyes were located.',
  hf: 'FFT upper-mid frequency share on the eye band. More sensitive to slight softness; it must also clear its threshold for eye-band photos.',
  head: 'Laplacian on the head box. Used only when no eyes were located (helmet, visor, turned away). Photos with eyes still list a head value, but it does not affect their tier.',
};

function Matrix({ rows, title, accuracy, tip }: { rows: TruthMatrixRow[]; title: string; accuracy: number | null; tip: string }) {
  const cell = (t: number, p: number) => rows.find((r) => r.truth === t && r.pred === p)?.n ?? '';
  return (
    <div className="rounded-lg border border-gray-800 bg-gray-900 p-3">
      <div className="text-xs uppercase tracking-wide text-gray-500 mb-1"><Tip tip={tip}>{title}</Tip> {accuracy !== null && <Tip tip="Share of photos with a truth tier where this source picked exactly the same tier (the diagonal). Rows are your tier, columns the prediction: above the diagonal is too generous, below is too strict." className="text-gray-300 normal-case">· agreement {Math.round(accuracy * 100)}%</Tip>}</div>
      <div className="overflow-x-auto"><table className="text-xs">
        <thead><tr><th className="text-gray-600 font-normal pr-2 text-left">truth ↓ / predicted →</th>{TIERS.map((p) => <th key={p} className="px-3 text-gray-400">{p}</th>)}</tr></thead>
        <tbody>{TIERS.map((t) => (
          <tr key={t}><td className="pr-2 text-gray-400">{t}</td>{TIERS.map((p) => <td key={p} className={`px-3 text-center tabular-nums ${t === p ? 'text-emerald-300' : 'text-gray-300'}`}>{cell(t, p)}</td>)}</tr>
        ))}</tbody>
      </table></div>
    </div>
  );
}

function GroundTruth({ onApply, applying }: { onApply: (values: Record<string, number>) => void; applying: boolean }) {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['truth'], queryFn: api.truth });
  const [dir, setDir] = useState('');
  const [folder, setFolder] = useState('');
  const [msg, setMsg] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const done = (r: { verdicts: number; matched: number; unmatched: number }) => {
    setMsg(`${r.verdicts} verdicts read, ${r.matched} matched to tracked images, ${r.unmatched} unmatched (not scanned yet, or different filename)`);
    qc.invalidateQueries({ queryKey: ['truth'] }); qc.invalidateQueries({ queryKey: ['images'] });
  };
  const upload = useMutation({ mutationFn: (files: FileList) => api.truthUpload(files, folder), onSuccess: done, onError: (e) => setMsg(errMsg(e)) });
  const imp = useMutation({ mutationFn: () => api.truthImport(dir, folder), onSuccess: done, onError: (e) => setMsg(errMsg(e)) });
  const clear = useMutation({ mutationFn: api.truthClear, onSuccess: () => { setMsg('cleared'); qc.invalidateQueries({ queryKey: ['truth'] }); } });
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm';
  const s: TruthSummary | undefined = data;
  return (
    <section className="space-y-3 rounded-lg border border-gray-800 p-3 sm:p-4">
      <h2 className="font-semibold">Ground truth (your exported verdicts)</h2>
      <p className="text-sm text-gray-400 max-w-3xl">Export known-good metadata from Lightroom (select photos → Metadata → Save Metadata to File, or export the sidecars), then upload the <code>.xmp</code> files (or a <code>.zip</code>, or a <code>.csv</code> with <code>name,rating,label,focus_tier,keywords</code>). Files are matched to tracked images by filename. A focus tier is taken from a <code>focus:3</code>-style keyword or an explicit CSV column first, otherwise from the color label ({s?.mapping ? Object.entries(s.mapping.label_tiers).map(([k, v]) => `${k}→${v}`).join(', ') : '…'}), otherwise from the star rating ({s?.mapping ? Object.entries(s.mapping.rating_tiers).map(([k, v]) => `${k}★→${v ?? '–'}`).join(', ') : '…'}).</p>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="limit matching to photos subfolder (optional)" className={`${sel} w-full sm:w-80`} />
        <input ref={fileRef} type="file" multiple accept=".xmp,.xml,.csv,.zip" className="text-xs max-w-full" />
        <button onClick={() => fileRef.current?.files?.length && upload.mutate(fileRef.current.files)} disabled={upload.isPending} className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">upload</button>
        <span className="text-gray-600">or</span>
        <input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="folder on the photos or data volume" className={`${sel} w-full sm:w-72`} />
        <button onClick={() => imp.mutate()} disabled={!dir || imp.isPending} className="px-3 py-1 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40">import from folder</button>
        {s && s.images_with_truth > 0 && <button onClick={() => clear.mutate()} className="ml-auto text-xs text-gray-500 hover:text-red-300">clear truth</button>}
      </div>
      {msg && <div className="text-xs text-gray-400">{msg}</div>}
      {s && s.images_with_truth > 0 && (
        <div className="space-y-3">
          <div className="text-sm text-gray-300">{s.images_with_truth} images have verdicts, {s.with_tier} with a focus tier. <Link to="/photos?truth_mismatch=1" className="text-blue-400 hover:underline">show mismatches →</Link></div>
          <div className="grid md:grid-cols-2 gap-3">
            <Matrix rows={s.local.matrix} title="local sharpness tier" accuracy={s.local.accuracy} tip="Your tier vs the local tier from the current thresholds. This is the one the thresholds below change; re-score and it updates." />
            <Matrix rows={s.vlm.matrix} title="vision model tier" accuracy={s.vlm.accuracy} tip="Your tier vs the vision model's tier. Thresholds don't affect it; it only changes when the model re-tags." />
          </div>
          {Object.keys(s.suggested).length > 0 && (
            <div className="text-sm space-y-1">
              <div className="text-gray-400">Suggested thresholds from your verdicts (each picked for balanced accuracy on its own; with eyes found, a shot must clear both eye-band metrics):</div>
              {(Object.entries(s.suggested) as [FocusMetric, NonNullable<TruthSummary['suggested'][FocusMetric]>][]).map(([m, sug]) => (
                <div key={m} className="flex flex-wrap gap-3 pl-2">
                  <span className="w-full sm:w-64 text-gray-300"><Tip tip={METRIC_TAB_TIP[m]}>{FOCUS_METRIC_LABEL[m]}</Tip> <Tip tip="Photos that have both this metric and a truth tier. Under about 30, treat the suggestion as rough." className="text-gray-500">(n={sug.n})</Tip></span>
                  {Object.entries(sug).filter(([k]) => k !== 'n').map(([k, v]) => typeof v === 'object' && (
                    <Tip key={k} plain tip={<>The cut on this metric that best separates your {`tier-${k.match(/tier(\d)/)?.[1]}-or-better photos from the rest`}. Balanced accuracy is the average of the hit rate on each side, so a lopsided set can't inflate it. To be pickier than your own labels, round tier 3 up.</>}><span className="font-mono text-xs cursor-help">{k} ≥ <b>{v.value}</b> <span className="text-gray-500">({Math.round(v.balanced_accuracy * 100)}% balanced acc.)</span></span></Tip>
                  ))}
                </div>
              ))}
              <button disabled={applying} onClick={() => onApply(Object.fromEntries(Object.values(s.suggested).flatMap((sug) => Object.entries(sug ?? {}).flatMap(([k, v]) => (typeof v === 'object' && v ? [[k, v.value]] : []))))) } className="px-3 py-1 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40">use all + re-score</button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

export default function Calibrate() {
  const qc = useQueryClient();
  const [metric, setMetric] = useState<FocusMetric>('eye');
  const { data } = useQuery({ queryKey: ['calibration', metric], queryFn: () => api.calibration(48, metric) });
  // Inputs show the server thresholds until edited (keyed by config name; missing = not edited yet).
  const [edits, setEdits] = useState<Record<string, string>>({});
  const keys = data?.keys ?? ['', '', ''];   // tier 3, 2, 1
  const shown = (k: string) => edits[k] ?? String(data?.thresholds?.[k] ?? '');
  const save = useMutation({
    mutationFn: async (values: Record<string, number>) => { await api.putConfig({ focus: values }); return api.rescore(); },
    onSuccess: () => { setEdits({}); qc.invalidateQueries(); },
  });
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm w-28';
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-semibold">Calibrate</h1>
      <GroundTruth onApply={(values) => save.mutate(values)} applying={save.isPending} />
      <h2 className="font-semibold">Local focus thresholds</h2>
      <p className="text-sm text-gray-400 max-w-3xl">Focus is judged on a band across both eyes when they can be located (face landmarks, else the pose model's eye keypoints). There the eye band must clear two thresholds: the contrast-normalized Laplacian and the FFT detail ratio, which drops faster for slight softness. When no eyes are found (helmet, visor, turned away), the head-box Laplacian is used. Crops below are the primary subject ordered softest to sharpest by the chosen metric. Find where a miss becomes soft, soft becomes slightly soft, and slightly soft becomes sharp, enter those three numbers, and re-score. This only affects the <em>local</em> tier. Metrics added after an image was analyzed need a fresh local pass on it.</p>
      <div className="flex flex-wrap gap-1 text-sm">
        {(Object.keys(FOCUS_METRIC_LABEL) as FocusMetric[]).map((m) => (
          <Tip key={m} plain tip={METRIC_TAB_TIP[m]}><SegButton on={m === metric} onClick={() => setMetric(m)} className="px-3 py-1">{FOCUS_METRIC_LABEL[m]}</SegButton></Tip>
        ))}
      </div>
      {data?.percentiles && (
        <Tip tip="Distribution of this metric over the primary subject of every analyzed photo. p50 is the median, and p90 means 90% of photos score at or below it. If the current tier-3 cut sits below p25, most photos pass it and the tier isn't picky." className="text-xs text-gray-400 font-mono break-words">{data.count ?? 0} images · {Object.entries(data.percentiles).map(([k, v]) => `${k}=${v}`).join('  ')}</Tip>
      )}
      {keys[0] && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-sm">
          {[...keys].reverse().map((k, i) => (
            <span key={k} className="inline-flex items-center gap-2">
              <span className="text-gray-500">tier {i + 1} ≥</span><input value={shown(k)} onChange={(e) => setEdits({ ...edits, [k]: e.target.value })} className={sel} />
            </span>
          ))}
          <button onClick={() => save.mutate(Object.fromEntries(keys.map((k) => [k, Number(shown(k))])))} disabled={save.isPending} className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">save + re-score</button>
          {save.isPending && <span className="text-gray-500">re-scoring…</span>}
          {save.data && <RescoreSummary r={save.data} />}
          {save.error && <span className="text-red-400">re-score failed: {errMsg(save.error)}</span>}
        </div>
      )}
      <div className="grid gap-2 grid-cols-[repeat(auto-fill,minmax(110px,1fr))] sm:grid-cols-[repeat(auto-fill,minmax(150px,1fr))]">
        {data?.samples.map((s) => (
          <div key={s.id} className="rounded-lg overflow-hidden border border-gray-800 bg-gray-900">
            <img src={cropUrl(s.id)} alt="" loading="lazy" className="w-full aspect-square object-cover" />
            <div className="px-2 py-1 text-xs font-mono flex justify-between"><span>{s.sharp.toFixed(4)}</span><span style={{ color: TIER_COLOR[s.tier] }}>tier {s.tier}</span></div>
          </div>
        ))}
      </div>
    </div>
  );
}

function RescoreSummary({ r }: { r: RescoreResult }) {
  return (
    <span className="text-gray-400">
      {r.changed} images changed tier · AF points read on {r.af_backfilled} · primary re-picked on {r.primary_changed}
      {r.errors > 0 && <span className="text-amber-400" title={r.first_error ?? ''}> · {r.errors} failed (hover for the first)</span>}
    </span>
  );
}
