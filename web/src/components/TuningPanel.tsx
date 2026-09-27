import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { RescoreResult } from '../api/client';
import { errMsg } from '../lib/format';
import { APPLIES_TEXT, GROUPS, fmtShutter, getPath, nest, otherKeys, parseShutter } from '../lib/tuning';
import type { Applies, Param } from '../lib/tuning';
import Tip from './Tip';

const input = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm w-24 tabular-nums';
const APPLIES_CLS: Record<Applies, string> = {
  rescore: 'text-emerald-300 border-emerald-800', reanalyze: 'text-amber-300 border-amber-800',
  retag: 'text-violet-300 border-violet-800', live: 'text-sky-300 border-sky-800', info: 'text-gray-400 border-gray-700',
};

export function AppliesChip({ a }: { a: Applies }) {
  return <Tip plain tip={APPLIES_TEXT[a].tip}><span className={`text-[10px] uppercase tracking-wide border rounded px-1 py-px cursor-help ${APPLIES_CLS[a]}`}>{APPLIES_TEXT[a].short}</span></Tip>;
}

export function StricterChip({ dir }: { dir: 'higher' | 'lower' }) {
  return (
    <Tip plain tip={dir === 'higher' ? 'Raising this makes the local tier pickier: fewer photos keep a high tier.' : 'Lowering this makes the local tier pickier: fewer photos keep a high tier.'}>
      <span className="text-[10px] whitespace-nowrap text-gray-300 bg-gray-800 rounded px-1 py-px cursor-help">{dir === 'higher' ? '↑' : '↓'} stricter</span>
    </Tip>
  );
}

const show = (p: Param | undefined, v: unknown) =>
  v == null ? '' : p?.display === 'shutter' && typeof v === 'number' ? fmtShutter(v) : String(v);

/** Parse an edited text back to the config's type; undefined when it isn't valid. */
function parse(p: Param | undefined, text: string, like: unknown): unknown {
  if (p?.display === 'shutter') { const n = parseShutter(text); return Number.isFinite(n) && n > 0 ? n : undefined; }
  if (typeof like === 'number' || p?.kind === 'number') {
    const n = Number(text);
    if (text.trim() === '' || !Number.isFinite(n)) return undefined;
    if ((p?.min != null && n < p.min) || (p?.max != null && n > p.max)) return undefined;
    return n;
  }
  return text;
}

/** One knob: input (or toggle / menu), what raising it does, which way is stricter, when it applies, its default. */
function Row({ p, k, cur, def, edit, setEdit }: {
  p?: Param; k: string; cur: unknown; def: unknown; edit: { text?: string; value: unknown } | undefined;
  setEdit: (e: { text?: string; value: unknown } | undefined) => void;
}) {
  const value = edit ? edit.value : cur;
  const bad = edit !== undefined && edit.value === undefined;
  const changed = edit !== undefined && !bad && JSON.stringify(edit.value) !== JSON.stringify(cur);
  const kind = p?.kind ?? (typeof cur === 'boolean' ? 'bool' : typeof cur === 'number' ? 'number' : 'text');
  const off = p?.nullable && value === null;
  const differsFromDefault = def !== undefined && JSON.stringify(value) !== JSON.stringify(def);
  const set = (v: unknown, text?: string) => setEdit(JSON.stringify(v) === JSON.stringify(cur) && text === undefined ? undefined : { value: v, text });
  let control;
  if (kind === 'bool') {
    control = (
      <button onClick={() => set(!value)} aria-pressed={!!value}
        className={`w-12 rounded-full border px-1 py-0.5 text-xs ${value ? 'bg-emerald-700/60 border-emerald-500 text-emerald-100' : 'bg-gray-800 border-gray-700 text-gray-400'}`}>
        {value ? 'on' : 'off'}
      </button>
    );
  } else if (kind === 'select' && p?.options) {
    control = (
      <select value={String(value)} onChange={(e) => { const o = p.options!.find((x) => String(x.value) === e.target.value); set(o?.value); }}
        className="bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm max-w-full">
        {p.options.map((o) => <option key={o.value} value={String(o.value)}>{o.label}</option>)}
      </select>
    );
  } else {
    control = (
      <span className="inline-flex items-center gap-1.5">
        {!off && (
          <input value={edit?.text ?? show(p, value)} inputMode="decimal"
            onChange={(e) => setEdit({ text: e.target.value, value: parse(p, e.target.value, cur) })}
            className={`${input} ${bad ? 'border-red-500' : changed ? 'border-blue-500' : ''}`} />
        )}
        {p?.nullable && (
          <label className="inline-flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" checked={!!off} onChange={(e) => set(e.target.checked ? null : (cur ?? def ?? p.whenOn ?? 0))} /> off
          </label>
        )}
        {p?.unit && !off && <span className="text-xs text-gray-500">{p.unit}</span>}
      </span>
    );
  }
  return (
    <div className="grid grid-cols-1 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)] gap-x-4 gap-y-1 py-2 border-t border-gray-800/70 first:border-t-0">
      <div className="text-sm">
        <Tip tip={<>{p?.help ?? 'No description for this key yet.'}<div className="mt-1 text-gray-500 font-mono">{k}</div></>}>
          <span className={changed ? 'text-blue-300' : 'text-gray-200'}>{p?.label ?? k}</span>
        </Tip>
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        {control}
        {p?.stricter && <StricterChip dir={p.stricter} />}
        {p && <AppliesChip a={p.applies} />}
        {differsFromDefault && (
          <button onClick={() => set(def)} className="text-xs text-gray-500 hover:text-gray-200">default {def === null ? 'off' : show(p, def)} ↺</button>
        )}
        {p?.up && <span className="basis-full text-xs text-gray-500">raise → {p.up}</span>}
      </div>
    </div>
  );
}

/** Every tuning knob in config.json apart from the focus tier cuts, grouped, with a single save. */
export default function TuningPanel() {
  const qc = useQueryClient();
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config });
  const { data: defs } = useQuery({ queryKey: ['config-defaults'], queryFn: api.configDefaults, staleTime: Infinity });
  const [edits, setEdits] = useState<Record<string, { text?: string; value: unknown }>>({});
  const [result, setResult] = useState<{ rescore?: RescoreResult; later: Applies[] } | null>(null);
  const byKey = useMemo(() => new Map(GROUPS.flatMap((g) => g.params.map((p) => [p.key, p] as const))), []);
  const other = useMemo(() => (cfg ? otherKeys(cfg) : []), [cfg]);

  const pending = Object.entries(edits).filter(([k, e]) => e.value !== undefined && JSON.stringify(e.value) !== JSON.stringify(getPath(cfg, k)));
  const invalid = Object.values(edits).some((e) => e.value === undefined);
  const save = useMutation({
    mutationFn: async () => {
      await api.putConfig(nest(Object.fromEntries(pending.map(([k, e]) => [k, e.value]))), 'tuning');
      const applies = new Set(pending.map(([k]) => byKey.get(k)?.applies ?? 'reanalyze'));
      const rescore = applies.has('rescore') ? await api.rescore('tuning') : undefined;
      return { rescore, later: (['reanalyze', 'retag'] as Applies[]).filter((a) => applies.has(a)) };
    },
    onSuccess: (r) => { setEdits({}); setResult(r); qc.invalidateQueries(); },
  });

  if (!cfg) return null;
  const row = (k: string) => (
    <Row key={k} k={k} p={byKey.get(k)} cur={getPath(cfg, k)} def={getPath(defs, k)} edit={edits[k]}
      setEdit={(e) => setEdits((all) => { const next = { ...all }; if (e) next[k] = e; else delete next[k]; return next; })} />
  );
  return (
    <section className="space-y-3">
      <div>
        <h2 className="font-semibold">Focus rules and other tuning</h2>
        <p className="text-sm text-gray-400 max-w-3xl">
          Everything else that shapes a tier. Hover a name for what it does. <StricterChip dir="higher" /> / <StricterChip dir="lower" /> say which way makes the local tier pickier; the colored tag says when a change reaches your photos:
          {' '}<AppliesChip a="rescore" /> right after saving, <AppliesChip a="reanalyze" /> on a photo's next local pass, <AppliesChip a="retag" /> when the model tags it again, <AppliesChip a="live" /> at once, <AppliesChip a="info" /> never on the local tier.
        </p>
      </div>
      <div className="grid gap-3 lg:grid-cols-2">
        {GROUPS.map((g) => (
          <div key={g.title} className="rounded-lg border border-gray-800 bg-gray-900/40 p-3">
            <h3 className="text-sm font-semibold text-gray-200">{g.title}</h3>
            <p className="text-xs text-gray-500 mb-1">{g.intro}</p>
            {g.params.map((p) => row(p.key))}
          </div>
        ))}
      </div>
      {other.length > 0 && (
        <details className="rounded-lg border border-gray-800 bg-gray-900/40 p-3">
          <summary className="text-sm font-semibold text-gray-200 cursor-pointer">Other settings ({other.length})</summary>
          <p className="text-xs text-gray-500">Config keys without a description yet. Assume a change needs a fresh local pass.</p>
          {other.map(row)}
        </details>
      )}
      <div className="sticky bottom-0 z-10 -mx-3 sm:mx-0 flex flex-wrap items-center gap-3 rounded-lg border border-gray-800 bg-gray-950/95 px-3 py-2 text-sm backdrop-blur">
        <button onClick={() => save.mutate()} disabled={!pending.length || invalid || save.isPending}
          className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">
          {save.isPending ? 'saving…' : `save ${pending.length || ''} change${pending.length === 1 ? '' : 's'}`}
        </button>
        {pending.length > 0 && <button onClick={() => setEdits({})} className="text-gray-400 hover:text-gray-100">discard</button>}
        {invalid && <span className="text-red-400">a value is out of range or not a number</span>}
        {save.error && <span className="text-red-400">save failed: {errMsg(save.error)}</span>}
        {result && !pending.length && (
          <span className="text-gray-400">
            saved{result.rescore && <> · re-scored: {result.rescore.changed} photos changed tier</>}
            {result.later.includes('reanalyze') && <> · re-analyze photos for the <AppliesChip a="reanalyze" /> changes</>}
            {result.later.includes('retag') && <> · re-tag for the <AppliesChip a="retag" /> changes</>}
          </span>
        )}
      </div>
    </section>
  );
}
