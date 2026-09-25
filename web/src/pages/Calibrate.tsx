import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, cropUrl } from '../api/client';

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
      <h1 className="text-lg font-semibold">Calibrate local focus thresholds</h1>
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
