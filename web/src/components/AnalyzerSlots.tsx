import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import { errMsg } from '../lib/format';
import Tip from './Tip';

/** The analyzer pool's size, and how many images each pod takes at once (config analyzer_slots). Only shown with a
 * pool of analyzer pods; a single analyzer goes by the config's workers. */
export default function AnalyzerSlots() {
  const qc = useQueryClient();
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: api.health, refetchInterval: 10000 });
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config });
  const saved = Number(cfg?.analyzer_slots ?? 0);
  const [draft, setDraft] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: (n: number) => api.putConfig({ analyzer_slots: n }, 'analyzer slots'),
    onSuccess: (c) => { qc.setQueryData(['config'], c); setDraft(null); qc.invalidateQueries({ queryKey: ['health'] }); },
  });
  if (health?.analyzer_pods == null) return null;
  const own = health.analyzer_pod_slots;
  const value = draft ?? String(saved);
  const n = Math.max(0, Math.floor(Number(value)));
  const dirty = draft != null && Number.isFinite(n) && n !== saved;
  return (
    <div className="rounded-lg border border-gray-800 p-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
      <span className="text-gray-400">
        Analyzer pool: <span className="text-gray-200">{health.analyzer_pods} {health.analyzer_pods === 1 ? 'pod' : 'pods'}</span> ·{' '}
        <span className="text-gray-200">{health.analyzer_slots ?? 0} slots</span>
      </span>
      <label className="flex items-center gap-2 text-gray-500">
        <Tip tip={<>Images each analyzer pod works on at once. 0 uses the pod's own <code>ANALYZER_SLOTS</code>{own != null ? ` (${own})` : ''}.
          Applies right away, also to a running job. Each image in flight holds a decoded full-resolution frame, so going
          above the pod's own count needs its memory limit raised too.</>}>per pod</Tip>
        <input type="number" min={0} step={1} value={value} onChange={(e) => setDraft(e.target.value)}
          className="w-16 bg-gray-900 border border-gray-700 rounded px-2 py-1" />
        {saved === 0 && own != null && <span className="text-xs">(pod default {own})</span>}
      </label>
      {dirty && <button onClick={() => save.mutate(n)} disabled={save.isPending}
        className="px-3 py-1 rounded border border-gray-700 hover:border-gray-500 disabled:opacity-40">{save.isPending ? 'saving…' : 'Save'}</button>}
      {save.error && <span className="text-red-400 text-xs">{errMsg(save.error)}</span>}
    </div>
  );
}
