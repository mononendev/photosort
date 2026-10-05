import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { ConfigHistoryEntry } from '../api/client';
import { errMsg } from '../lib/format';
import { GROUPS } from '../lib/tuning';
import Tip from './Tip';

const val = (v: unknown) =>
  v === undefined ? '—' : v === null ? 'off' : typeof v === 'number' ? String(Number(v.toPrecision(4))) : JSON.stringify(v);
const when = (at: string) => new Date(at.replace(/([+-]\d\d)(\d\d)$/, '$1:$2')).toLocaleString();

/** Every config save, newest first, with what it changed; roll back to the settings from just before any of them. */
export default function ConfigHistory() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['config-history'], queryFn: api.configHistory });
  const [open, setOpen] = useState(5);
  const label = useMemo(() => new Map(GROUPS.flatMap((g) => g.params.map((p) => [p.key, p.label] as const))), []);
  const restore = useMutation({
    mutationFn: async (id: number) => { await api.configRestore(id); return api.rescore('rollback'); },
    onSuccess: () => qc.invalidateQueries(),
  });
  // A re-score is shown on the save it followed rather than as its own row; a manual one followed no save.
  const rows = useMemo(() => {
    const out: { e: Extract<ConfigHistoryEntry, { kind: 'change' }>; rescored?: number }[] = [];
    let pending: number | undefined;
    for (const e of data ?? []) {
      if (e.kind === 'rescore') { if (e.source !== 'manual') pending ??= e.changed; continue; }
      out.push({ e, rescored: pending }); pending = undefined;
    }
    return out;
  }, [data]);

  if (!rows.length) return null;
  return (
    <section className="space-y-2">
      <div>
        <h2 className="font-semibold">History</h2>
        <p className="text-sm text-gray-400 max-w-3xl">Every save here is logged with the whole config as it stood just before. Roll back puts all settings back to that point and re-scores; the rollback is logged too, so it can be undone the same way.</p>
      </div>
      <ol className="space-y-2">
        {rows.slice(0, open).map(({ e, rescored }) => (
          <li key={e.id} className="rounded-lg border border-gray-800 bg-gray-900/40 p-3 text-sm">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="text-gray-200">{when(e.at)}</span>
              <span className="text-xs uppercase tracking-wide border border-gray-700 rounded px-1 py-px text-gray-400">{e.source}</span>
              <span className="text-gray-500">{e.changes.length} change{e.changes.length === 1 ? '' : 's'}{rescored !== undefined && <> · re-scored: {rescored} photos changed tier</>}</span>
              <Tip plain tip="Put every setting back to how it was just before this save, then re-score.">
                <button disabled={restore.isPending} onClick={() => confirm(`Roll every setting back to before the ${e.source} save of ${when(e.at)}?`) && restore.mutate(e.id)}
                  className="ml-auto px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-xs">roll back to before this</button>
              </Tip>
            </div>
            <ul className="mt-1 grid gap-x-4 sm:grid-cols-2 text-xs">
              {e.changes.map((c) => (
                <li key={c.key} className="flex gap-2 min-w-0">
                  <Tip plain tip={c.key}><span className="text-gray-400 truncate">{label.get(c.key) ?? c.key}</span></Tip>
                  <span className="font-mono tabular-nums whitespace-nowrap"><span className="text-gray-500">{val(c.from)}</span> → <span className="text-gray-200">{val(c.to)}</span></span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ol>
      {restore.error && <div className="text-sm text-red-400">rollback failed: {errMsg(restore.error)}</div>}
      {rows.length > open && <button onClick={() => setOpen((n) => n + 20)} className="text-sm text-gray-400 hover:text-gray-100">show older ({rows.length - open} more)</button>}
    </section>
  );
}
