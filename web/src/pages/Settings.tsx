import { useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, FOCUS_METRIC_LABEL, TIERS } from '../api/client';
import type { FocusMetric, TruthMatrixRow, TruthSource, TruthSummary } from '../api/client';
import ConfigHistory from '../components/ConfigHistory';
import FocusThresholds from '../components/FocusThresholds';
import SegButton from '../components/SegButton';
import TuningPanel from '../components/TuningPanel';
import PoseModelPanel from '../components/PoseModelPanel';
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
  const [source, setSource] = useState<TruthSource>('both');
  const { data } = useQuery({ queryKey: ['truth', source], queryFn: () => api.truth(source) });
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
  const clear = useMutation({ mutationFn: api.truthClear, onSuccess: () => { setMsg('imported verdicts cleared'); qc.invalidateQueries({ queryKey: ['truth'] }); } });
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm';
  const s: TruthSummary | undefined = data;
  return (
    <section className="space-y-3 rounded-lg border border-gray-800 p-3 sm:p-4">
      <h2 className="font-semibold">Ground truth (your ratings and exported verdicts)</h2>
      <p className="text-sm text-gray-400 max-w-3xl">Every photo you rate with q/w/e/r/t is a verdict: the more you cull, the better the suggestions below get (a ★ banger counts as sharp). They're worked out when you open this page; nothing changes until you apply them. You can also import verdicts from Lightroom.</p>
      <div className="flex flex-wrap items-center gap-1 text-sm">
        <span className="text-gray-500 mr-1">use</span>
        {([['both', 'ratings + imported'], ['ratings', 'your ratings'], ['imported', 'imported only']] as [TruthSource, string][]).map(([k, label]) => (
          <Tip key={k} plain tip={k === 'both' ? 'Your in-app ratings, plus imported verdicts for photos you haven’t rated. Where a photo has both, your rating wins.' : k === 'ratings' ? 'Only photos you rated here (q/w/e/r/t).' : 'Only verdicts imported from Lightroom sidecars or a CSV.'}>
            <SegButton on={source === k} onClick={() => setSource(k)} className="px-3 py-1">{label}</SegButton>
          </Tip>
        ))}
        {s && <span className="ml-2 text-xs text-gray-500">{s.rated} rated here · {s.imported} imported</span>}
      </div>
      <p className="text-sm text-gray-400 max-w-3xl">To import: export known-good metadata from Lightroom (select photos → Metadata → Save Metadata to File, or export the sidecars), then upload the <code>.xmp</code> files (or a <code>.zip</code>, or a <code>.csv</code> with <code>name,rating,label,focus_tier,keywords</code>). Files are matched to tracked images by filename. A focus tier is taken from a <code>focus:3</code>-style keyword or an explicit CSV column first, otherwise from the color label ({s?.mapping ? Object.entries(s.mapping.label_tiers).map(([k, v]) => `${k}→${v}`).join(', ') : '…'}), otherwise from the star rating ({s?.mapping ? Object.entries(s.mapping.rating_tiers).map(([k, v]) => `${k}★→${v ?? '–'}`).join(', ') : '…'}).</p>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="limit matching to photos subfolder (optional)" className={`${sel} w-full sm:w-80`} />
        <input ref={fileRef} type="file" multiple accept=".xmp,.xml,.csv,.zip" className="text-xs max-w-full" />
        <button onClick={() => fileRef.current?.files?.length && upload.mutate(fileRef.current.files)} disabled={upload.isPending} className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">upload</button>
        <span className="text-gray-600">or</span>
        <input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="folder on the photos or data volume" className={`${sel} w-full sm:w-72`} />
        <button onClick={() => imp.mutate()} disabled={!dir || imp.isPending} className="px-3 py-1 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40">import from folder</button>
        {s && s.imported > 0 && <Tip plain tip="Forget the imported verdicts. Your ratings stay."><button onClick={() => clear.mutate()} className="ml-auto text-xs text-gray-500 hover:text-red-300">clear imported</button></Tip>}
      </div>
      {msg && <div className="text-xs text-gray-400">{msg}</div>}
      {s && s.images_with_truth > 0 && (
        <div className="space-y-3">
          <div className="text-sm text-gray-300">{s.images_with_truth} images have verdicts, {s.with_tier} with a focus tier.{s.imported > 0 && <> <Link to="/photos?truth_mismatch=1" className="text-blue-400 hover:underline">show imported mismatches →</Link></>}</div>
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

export default function Settings() {
  const qc = useQueryClient();
  const save = useMutation({
    mutationFn: async ({ values, source }: { values: Record<string, number>; source: string }) => {
      await api.putConfig({ focus: values }, source); return api.rescore(source);
    },
    onSuccess: () => qc.invalidateQueries(),
  });
  const rescore = useMutation({ mutationFn: () => api.rescore('manual'), onSuccess: () => qc.invalidateQueries() });
  const r = rescore.data;
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h1 className="text-lg font-semibold">Settings</h1>
        <Tip plain tip="Re-apply the current settings to every analyzed photo without changing any: re-picks the primary, re-tiers from the stored numbers, and reads AF points and EXIF where missing. Needed after an update changes the rules. Crops and pixel metrics stay as analyzed.">
          <button disabled={rescore.isPending} onClick={() => rescore.mutate()}
            className="px-3 py-1 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-sm">{rescore.isPending ? 're-scoring…' : 're-score all'}</button>
        </Tip>
        {r && <span className="text-sm text-gray-400">{r.changed} photos changed tier · AF points read on {r.af_backfilled} · primary re-picked on {r.primary_changed}{r.errors > 0 && <span className="text-amber-400" title={r.first_error ?? ''}> · {r.errors} failed</span>}</span>}
        {rescore.error && <span className="text-sm text-red-400">re-score failed: {errMsg(rescore.error)}</span>}
      </div>
      <GroundTruth onApply={(values) => save.mutate({ values, source: 'auto-calibrate' })} applying={save.isPending} />
      <FocusThresholds onSave={(values) => save.mutate({ values, source: 'focus cuts' })} saving={save.isPending} saved={save.data} error={save.error} />
      <TuningPanel />
      <PoseModelPanel />
      <ConfigHistory />
    </div>
  );
}
