import { useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, cropUrl } from '../api/client';
import type { TruthMatrixRow, TruthSummary } from '../api/client';

function Matrix({ rows, title, accuracy }: { rows: TruthMatrixRow[]; title: string; accuracy: number | null }) {
  const cell = (t: number, p: number) => rows.find((r) => r.truth === t && r.pred === p)?.n ?? '';
  return (
    <div className="rounded-lg border border-gray-800 bg-gray-900 p-3">
      <div className="text-xs uppercase tracking-wide text-gray-500 mb-1">{title} {accuracy !== null && <span className="text-gray-300 normal-case">· agreement {Math.round(accuracy * 100)}%</span>}</div>
      <table className="text-xs">
        <thead><tr><th className="text-gray-600 font-normal pr-2 text-left">truth ↓ / predicted →</th>{[0, 1, 2].map((p) => <th key={p} className="px-3 text-gray-400">{p}</th>)}</tr></thead>
        <tbody>{[0, 1, 2].map((t) => (
          <tr key={t}><td className="pr-2 text-gray-400">{t}</td>{[0, 1, 2].map((p) => <td key={p} className={`px-3 text-center tabular-nums ${t === p ? 'text-emerald-300' : 'text-gray-300'}`}>{cell(t, p)}</td>)}</tr>
        ))}</tbody>
      </table>
    </div>
  );
}

function GroundTruth({ onApply }: { onApply: (t1?: number, t2?: number) => void }) {
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
  const upload = useMutation({ mutationFn: (files: FileList) => api.truthUpload(files, folder), onSuccess: done, onError: (e) => setMsg((e as Error).message) });
  const imp = useMutation({ mutationFn: () => api.truthImport(dir, folder), onSuccess: done, onError: (e) => setMsg((e as Error).message) });
  const clear = useMutation({ mutationFn: api.truthClear, onSuccess: () => { setMsg('cleared'); qc.invalidateQueries({ queryKey: ['truth'] }); } });
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm';
  const s: TruthSummary | undefined = data;
  return (
    <section className="space-y-3 rounded-lg border border-gray-800 p-4">
      <h2 className="font-semibold">Ground truth (your exported verdicts)</h2>
      <p className="text-sm text-gray-400 max-w-3xl">Export known-good metadata from Lightroom (select photos → Metadata → Save Metadata to File, or export the sidecars), then upload the <code>.xmp</code> files (or a <code>.zip</code>, or a <code>.csv</code> with <code>name,rating,label,focus_tier,keywords</code>). Files are matched to tracked images by filename. A focus tier is taken from a <code>focus:2</code>-style keyword or an explicit CSV column first, otherwise from the color label ({s?.mapping ? Object.entries(s.mapping.label_tiers).map(([k, v]) => `${k}→${v}`).join(', ') : '…'}), otherwise from the star rating ({s?.mapping ? Object.entries(s.mapping.rating_tiers).map(([k, v]) => `${k}★→${v ?? '–'}`).join(', ') : '…'}).</p>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="limit matching to photos subfolder (optional)" className={`${sel} w-80`} />
        <input ref={fileRef} type="file" multiple accept=".xmp,.xml,.csv,.zip" className="text-xs" />
        <button onClick={() => fileRef.current?.files?.length && upload.mutate(fileRef.current.files)} disabled={upload.isPending} className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">upload</button>
        <span className="text-gray-600">or</span>
        <input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="folder on the photos or data volume" className={`${sel} w-72`} />
        <button onClick={() => imp.mutate()} disabled={!dir || imp.isPending} className="px-3 py-1 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40">import from folder</button>
        {s && s.images_with_truth > 0 && <button onClick={() => clear.mutate()} className="ml-auto text-xs text-gray-500 hover:text-red-300">clear truth</button>}
      </div>
      {msg && <div className="text-xs text-gray-400">{msg}</div>}
      {s && s.images_with_truth > 0 && (
        <div className="space-y-3">
          <div className="text-sm text-gray-300">{s.images_with_truth} images have verdicts, {s.with_tier} with a focus tier. <Link to="/photos?truth_mismatch=1" className="text-blue-400 hover:underline">show mismatches →</Link></div>
          <div className="grid md:grid-cols-2 gap-3">
            <Matrix rows={s.local.matrix} title="local sharpness tier" accuracy={s.local.accuracy} />
            <Matrix rows={s.vlm.matrix} title="vision model tier" accuracy={s.vlm.accuracy} />
          </div>
          {(s.suggested.tier1_min || s.suggested.tier2_min) && (
            <div className="text-sm flex flex-wrap items-center gap-3">
              <span className="text-gray-400">Suggested thresholds from your verdicts:</span>
              {s.suggested.tier1_min && <span>tier 1 ≥ <b>{s.suggested.tier1_min.value}</b> <span className="text-gray-500">(balanced acc. {Math.round(s.suggested.tier1_min.balanced_accuracy * 100)}%)</span></span>}
              {s.suggested.tier2_min && <span>tier 2 ≥ <b>{s.suggested.tier2_min.value}</b> <span className="text-gray-500">({Math.round(s.suggested.tier2_min.balanced_accuracy * 100)}%)</span></span>}
              <button onClick={() => onApply(s.suggested.tier1_min?.value, s.suggested.tier2_min?.value)} className="px-3 py-1 rounded bg-gray-800 hover:bg-gray-700">use these</button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

export default function Calibrate() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['calibration'], queryFn: () => api.calibration(48) });
  // Inputs show the server thresholds until edited (null = not edited yet).
  const [e1, setE1] = useState<string | null>(null);
  const [e2, setE2] = useState<string | null>(null);
  const t1 = e1 ?? String(data?.thresholds?.tier1_min ?? '');
  const t2 = e2 ?? String(data?.thresholds?.tier2_min ?? '');
  const setT1 = setE1;
  const setT2 = setE2;
  const save = useMutation({
    mutationFn: async () => { await api.putConfig({ focus: { tier1_min: Number(t1), tier2_min: Number(t2) } }); return api.rescore(); },
    onSuccess: () => { qc.invalidateQueries(); },
  });
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm w-28';
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-semibold">Calibrate</h1>
      <GroundTruth onApply={(a, b) => { if (a !== undefined) setE1(String(a)); if (b !== undefined) setE2(String(b)); }} />
      <h2 className="font-semibold">Local focus thresholds</h2>
      <p className="text-sm text-gray-400 max-w-3xl">Head crops below are ordered from softest to sharpest by the local sharpness metric (contrast-normalized Laplacian variance on the original pixels). Find where "soft" becomes "usable" and "usable" becomes "crisp", enter those two numbers, and re-score. This only affects the <em>local</em> tier; the vision model makes its own call and disagreements land in review.</p>
      {data?.percentiles && (
        <div className="text-xs text-gray-400 font-mono">{data.count} images · {Object.entries(data.percentiles).map(([k, v]) => `${k}=${v}`).join('  ')}</div>
      )}
      <div className="flex items-center gap-3 text-sm">
        <span className="text-gray-500">tier 1 ≥</span><input value={t1} onChange={(e) => setT1(e.target.value)} className={sel} />
        <span className="text-gray-500">tier 2 ≥</span><input value={t2} onChange={(e) => setT2(e.target.value)} className={sel} />
        <button onClick={() => save.mutate()} disabled={save.isPending} className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">save + re-score</button>
        {save.data && <span className="text-gray-400">{save.data.changed} images changed tier</span>}
      </div>
      <div className="grid gap-2 grid-cols-[repeat(auto-fill,minmax(150px,1fr))]">
        {data?.samples.map((s) => (
          <div key={s.id} className="rounded-lg overflow-hidden border border-gray-800 bg-gray-900">
            <img src={cropUrl(s.id)} alt="" loading="lazy" className="w-full aspect-square object-cover" />
            <div className="px-2 py-1 text-xs font-mono flex justify-between"><span>{s.sharp.toFixed(4)}</span><span className={s.tier === 2 ? 'text-emerald-300' : s.tier === 1 ? 'text-amber-300' : 'text-red-300'}>tier {s.tier}</span></div>
          </div>
        ))}
      </div>
    </div>
  );
}
