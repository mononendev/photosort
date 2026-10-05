import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { api, thumbUrl, TIER_CLASS, TIER_LABEL, TIERS } from '../api/client';
import type { ImageFilters, TraceKV, TraceNode, TracePerson, TraceStage, TraceTable } from '../api/client';
import FrameOverlay from '../components/FrameOverlay';
import Pager from '../components/Pager';
import { RatingBadge, TierBadge } from '../components/TierBadge';
import Tip from '../components/Tip';
import { gradePerson, PERSON_COLORS } from '../lib/pose';
import type { Layer } from '../lib/pose';

/** /trace picks a photo; /trace/:id shows every rule the pipeline ran on it, in order (photosort/trace.py). */
export default function Trace() {
  const { id } = useParams();
  return id ? <TraceView id={Number(id)} /> : <Picker />;
}

// ---- picker ---------------------------------------------------------------------------------------------------

const PAGE = 48;

function Picker() {
  const [sp, setSp] = useSearchParams();
  const navigate = useNavigate();
  const filters: ImageFilters = useMemo(() => ({
    q: sp.get('q') ?? undefined,
    folder: sp.get('folder') ?? '',
    tier: sp.get('tier') ? Number(sp.get('tier')) : undefined,
    review: sp.get('review') ? true : undefined,
    status: sp.get('status') ?? undefined,
    sort: sp.get('sort') ?? 'path',
    offset: Number(sp.get('offset') ?? 0),
    limit: PAGE,
  }), [sp]);
  const { data, isFetching } = useQuery({ queryKey: ['images', 'trace-picker', filters], queryFn: () => api.images(filters), placeholderData: keepPreviousData });
  const set = useCallback((k: string, v: string | undefined) => {
    setSp((cur) => { const n = new URLSearchParams(cur); if (v === undefined || v === '') n.delete(k); else n.set(k, v); if (k !== 'offset') n.delete('offset'); return n; }, { replace: true });
  }, [setSp]);
  const [draft, setDraft] = useState<string | null>(null);
  useEffect(() => {
    if (draft === null) return;
    const t = setTimeout(() => { set('q', draft); setDraft(null); }, 300);
    return () => clearTimeout(t);
  }, [draft, set]);
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1.5 sm:py-1 text-sm';
  const items = (data?.items ?? []).filter((x) => x.id !== null);
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-lg font-semibold">Trace a photo</h1>
        <p className="text-sm text-gray-400 max-w-3xl">Pick a photo to see every rule the pipeline ran on it, from the exposure lift through the local tier and the
          vision model to where the export puts it: what each rule read, whether it fired, and which one decided. Rules an earlier one made moot are shown too, with what they would have done.</p>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
        <input value={draft ?? filters.q ?? ''} onChange={(e) => setDraft(e.target.value)} placeholder="search keywords / name" className={`${sel} col-span-2 sm:w-64`} />
        <input value={filters.folder} onChange={(e) => set('folder', e.target.value)} placeholder="folder" className={`${sel} col-span-2 sm:w-56`} />
        <select value={filters.tier ?? ''} onChange={(e) => set('tier', e.target.value)} className={sel}>
          <option value="">any focus</option>{[...TIERS].reverse().map((t) => <option key={t} value={t}>{t} · {TIER_LABEL[t]}</option>)}
        </select>
        <select value={filters.status ?? ''} onChange={(e) => set('status', e.target.value)} className={sel}>
          <option value="">any status</option><option value="pending">pending</option><option value="analyzed">analyzed</option><option value="skipped">skipped</option><option value="tagged">tagged</option><option value="error">error</option>
        </select>
        <select value={filters.sort} onChange={(e) => set('sort', e.target.value === 'path' ? undefined : e.target.value)} className={sel}>
          <option value="path">by path</option><option value="newest">newest</option><option value="eye_sharpness">by eye sharpness</option><option value="sharpness">by head sharpness</option>
        </select>
        <label className="text-sm sm:text-xs text-gray-400 flex items-center gap-1.5"><input type="checkbox" checked={!!filters.review} onChange={(e) => set('review', e.target.checked ? '1' : undefined)} /> needs review</label>
        <span className="sm:ml-auto text-xs text-gray-500">{isFetching ? 'loading…' : `${data?.total ?? 0} photos`}</span>
      </div>
      <div className="grid gap-2 grid-cols-3 sm:grid-cols-[repeat(auto-fill,minmax(150px,1fr))]">
        {items.map((it) => (
          <button key={it.id} onClick={() => navigate(`/trace/${it.id}`)} title={it.rel}
            className="text-left rounded-lg overflow-hidden border border-gray-800 bg-gray-900 hover:border-blue-500 transition active:scale-[0.97]">
            <div className="aspect-[3/2] bg-gray-950 relative">
              {it.status !== 'pending' ? <img src={thumbUrl(it.id!)} alt="" loading="lazy" className="w-full h-full object-cover" />
                : <div className="w-full h-full flex items-center justify-center text-gray-700 text-xs">pending</div>}
              <span className="absolute top-1 left-1 flex gap-1">
                <TierBadge tier={it.focus_tier} small />
                {it.review && <span className="rounded bg-amber-900/80 text-amber-200 px-1 text-[10px]">review</span>}
                {it.reviewed && <RatingBadge rating={it.rating} />}
              </span>
            </div>
            <div className="px-2 py-1 text-xs truncate text-gray-300">{it.name}</div>
          </button>
        ))}
      </div>
      <Pager offset={filters.offset ?? 0} limit={PAGE} total={data?.total ?? 0} onPage={(o) => set('offset', String(o))} className="justify-center" />
    </div>
  );
}

// ---- visualization --------------------------------------------------------------------------------------------

const TRACE_LAYERS = new Set<Layer>(['af', 'people', 'regions', 'eyes']);

const fmtV = (v: unknown): string => {
  if (v === null || v === undefined || v === '') return '–';
  if (typeof v === 'boolean') return v ? 'yes' : 'no';
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : String(Number(v.toPrecision(4)));
  return String(v);
};

const STATE_DOT: Record<TraceStage['state'], string> = {
  done: 'bg-blue-500 border-blue-300', skipped: 'bg-gray-700 border-gray-500', pending: 'bg-gray-900 border-gray-600 border-dashed',
  error: 'bg-red-600 border-red-300', off: 'bg-gray-800 border-gray-600',
};
const STATE_TEXT: Record<TraceStage['state'], string> = {
  done: 'text-blue-300', skipped: 'text-gray-400', pending: 'text-gray-500', error: 'text-red-300', off: 'text-gray-500',
};

function TierPill({ tier, children }: { tier?: number | null; children: ReactNode }) {
  const cls = tier != null && TIER_CLASS[tier] ? TIER_CLASS[tier] : 'bg-gray-800 text-gray-200 border-gray-600';
  return <span className={`inline-block rounded border px-1.5 py-0.5 text-xs whitespace-nowrap ${cls}`}>{children}</span>;
}

function TraceView({ id }: { id: number }) {
  const { data: t, error, isLoading } = useQuery({ queryKey: ['trace', id], queryFn: () => api.trace(id) });
  const { data: img } = useQuery({ queryKey: ['image', id], queryFn: () => api.image(id) });
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config, staleTime: 30_000 });
  const [showMoot, setShowMoot] = useState(true);
  const [pick, setPick] = useState<{ id: number; person: number } | null>(null);
  const l = img?.local;
  const grades = useMemo(() => (l?.people ?? []).map((q) => gradePerson(q, cfg)), [l, cfg]);
  const person = pick?.id === id ? pick.person : Math.max(0, (t?.primary ?? 1) - 1);
  const select = useCallback((i: number) => setPick({ id, person: i }), [id]);
  const navigate = useNavigate();

  if (isLoading) return <p className="text-sm text-gray-500">loading…</p>;
  if (error || !t) return <p className="text-sm text-red-400">Couldn't load the trace: {String((error as Error | null)?.message ?? 'not found')}</p>;
  const { check } = t;
  const same = (a?: unknown[], b?: unknown[]) => JSON.stringify(a) === JSON.stringify(b);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Link to="/trace" className="text-sm text-gray-400 hover:text-white">← pick another</Link>
        <span className="font-mono text-sm text-gray-200 truncate min-w-0">{t.rel}</span>
        <span className="ml-auto flex items-center gap-3 text-xs">
          <label className="flex items-center gap-1.5 text-gray-400"><input type="checkbox" checked={showMoot} onChange={(e) => setShowMoot(e.target.checked)} /> show rules not reached</label>
          <Link to={`/photos?open=${id}`} className="text-blue-300 hover:text-blue-200">open photo →</Link>
        </span>
      </div>

      <PathStrip stages={t.stages} />

      {check.traced && check.engine && !same(check.traced, check.engine) && (
        <div className="rounded border border-red-700 bg-red-950/50 px-3 py-2 text-sm text-red-200">
          This walk reaches local {check.traced.join(' · ')}, but local_tier gives {check.engine.join(' · ')}. The trace has fallen behind the pipeline; the tier used everywhere else is local_tier's.
        </div>
      )}
      {check.engine && check.stored && !same(check.engine, check.stored) && (
        <div className="rounded border border-amber-700 bg-amber-950/40 px-3 py-2 text-sm text-amber-200">
          Traced with the current config: local {check.engine.join(' · ')}. The stored result is still {check.stored.join(' · ')} from the last re-score; saving the config on the Settings page re-scores.
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
        <div className="space-y-2 lg:sticky lg:top-[4.5rem] self-start min-w-0">
          <FrameOverlay id={id} l={l} grades={grades} layers={TRACE_LAYERS} selected={person} onSelect={select} onOpen={() => navigate(`/photos?open=${id}`)} />
          <div className="text-xs text-gray-500">Person numbers match the frame. Click a person here or a <b className="text-gray-400">#n</b> in the rules to highlight them.</div>
        </div>
        <ol className="relative min-w-0">
          {t.stages.map((s, i) => <StageCard key={s.key} s={s} n={i + 1} last={i === t.stages.length - 1} showMoot={showMoot} onPerson={(n) => select(n - 1)} selected={person + 1} />)}
        </ol>
      </div>
    </div>
  );
}

/** The route the photo took, one chip per stage: the outcome each stage handed to the next. */
function PathStrip({ stages }: { stages: TraceStage[] }) {
  const go = (k: string) => document.getElementById(`stage-${k}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  return (
    <div className="flex flex-wrap items-center gap-y-2 text-xs">
      {stages.map((s, i) => (
        <span key={s.key} className="flex items-center">
          {i > 0 && <span className={`mx-1 ${s.state === 'pending' ? 'text-gray-700' : 'text-gray-500'}`}>→</span>}
          <button onClick={() => go(s.key)} title={`${s.title}: ${s.summary}`}
            className={`rounded-md border px-2 py-1 text-left transition hover:border-gray-400 ${s.state === 'pending' ? 'border-dashed border-gray-700 text-gray-600'
              : s.outcome?.tier != null && TIER_CLASS[s.outcome.tier] ? TIER_CLASS[s.outcome.tier] : s.state === 'error' ? 'border-red-700 text-red-200' : 'border-gray-700 bg-gray-900 text-gray-200'}`}>
            <span className="block text-[10px] uppercase tracking-wide opacity-60">{s.title}</span>
            <span className="block max-w-[14rem] truncate">{s.outcome?.label ?? s.summary}</span>
          </button>
        </span>
      ))}
    </div>
  );
}

function StageCard({ s, n, last, showMoot, onPerson, selected }: {
  s: TraceStage; n: number; last: boolean; showMoot: boolean; onPerson: (n: number) => void; selected: number;
}) {
  const nodes = showMoot ? s.nodes : s.nodes.filter((x) => x.reached);
  const hidden = s.nodes.length - nodes.length;
  return (
    <li id={`stage-${s.key}`} className="relative pl-8 pb-5 scroll-mt-20">
      {!last && <span className="absolute left-[11px] top-6 bottom-0 w-px bg-gray-700" />}
      <span className={`absolute left-1 top-1.5 w-4 h-4 rounded-full border-2 ${STATE_DOT[s.state]}`} />
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="text-xs text-gray-600 tabular-nums">{n}</span>
        <h2 className="font-semibold text-gray-100">{s.title}</h2>
        <span className={`text-xs ${STATE_TEXT[s.state]}`}>{s.state}</span>
        <span className="text-sm text-gray-400 min-w-0 [overflow-wrap:anywhere]">{s.summary}</span>
        {s.outcome && s.state !== 'pending' && <span className="ml-auto"><TierPill tier={s.outcome.tier}>{s.outcome.label}</TierPill></span>}
      </div>
      {(s.facts.length > 0 || s.table || nodes.length > 0) && (
        <div className="mt-2 rounded-lg border border-gray-800 bg-gray-900/40 p-3 space-y-3">
          {s.facts.length > 0 && <Facts facts={s.facts} />}
          {s.table && <StageTable t={s.table} onPerson={onPerson} selected={selected} />}
          {nodes.length > 0 && <Decisions nodes={nodes} onPerson={onPerson} selected={selected} />}
          {hidden > 0 && <div className="text-[11px] text-gray-600">{hidden} rule{hidden > 1 ? 's' : ''} not reached hidden</div>}
        </div>
      )}
    </li>
  );
}

function Facts({ facts }: { facts: TraceKV[] }) {
  return (
    <div className="grid grid-cols-[100px_1fr] gap-x-3 gap-y-0.5 text-sm">
      {facts.map((f) => (
        <div key={f.k} className="contents">
          <span className={f.k === 'stale' || f.k === 'trace' ? 'text-amber-400' : 'text-gray-500'}>{f.k}</span>
          <span className="text-gray-200 [overflow-wrap:anywhere]">{fmtV(f.v)}{f.note && <span className="text-gray-500"> · {f.note}</span>}</span>
        </div>
      ))}
    </div>
  );
}

function PersonTag({ n, onPerson, selected }: { n: number; onPerson: (n: number) => void; selected: number }) {
  return (
    <button onClick={() => onPerson(n)} className={`font-mono rounded px-1 ${n === selected ? 'ring-1 ring-white/60' : ''}`}
      style={{ color: PERSON_COLORS[(n - 1) % PERSON_COLORS.length] }}>#{n}</button>
  );
}

function StageTable({ t, onPerson, selected }: { t: TraceTable; onPerson: (n: number) => void; selected: number }) {
  const th = 'text-left font-normal text-gray-500 px-2 py-1';
  const td = 'px-2 py-1 font-mono tabular-nums';
  if (t.kind === 'paths') {
    return (
      <div className="space-y-0.5">
        <div className="text-xs text-gray-500">placed in the export tree at</div>
        {t.rows.map((p) => <div key={p} className="font-mono text-sm text-gray-200 [overflow-wrap:anywhere]">{p}</div>)}
      </div>
    );
  }
  if (t.kind === 'grade') {
    return (
      <div className="overflow-x-auto">
        <table className="text-sm">
          <thead><tr><th className={th}>metric</th><th className={th}>value</th>{t.tiers.map((x) => <th key={x} className={th}>tier {x} cut</th>)}</tr></thead>
          <tbody>
            {t.rows.map((r) => (
              <tr key={r.label} className="border-t border-gray-800">
                <td className="px-2 py-1 text-gray-300">{r.label}</td>
                <td className={`${td} text-gray-100`}>{fmtV(r.value)}</td>
                {r.cuts.map((c, i) => (
                  <td key={i} className={`${td} ${r.ok[i] ? 'text-emerald-300 bg-emerald-950/40' : 'text-red-300/80 bg-red-950/30'}`}>
                    {r.ok[i] ? '✓' : '✗'} {fmtV(c)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        <div className="text-[11px] text-gray-500 mt-1">The grade is the highest tier whose column is all ✓.</div>
      </div>
    );
  }
  const cols = t.kind === 'people' ? ['person', 'conf', 'area', 'off-center', 'priority', 'head from'] : ['person', 'AF score', 'priority'];
  return (
    <div className="overflow-x-auto">
      <table className="text-sm">
        <thead><tr>{cols.map((c) => <th key={c} className={th}>{c}</th>)}</tr></thead>
        <tbody>
          {t.kind === 'people' ? t.rows.map((r) => (
            <tr key={r.n} className="border-t border-gray-800">
              <td className="px-2 py-1"><PersonTag n={r.n} onPerson={onPerson} selected={selected} /></td>
              <td className={td}>{fmtV(r.conf)}</td><td className={td}>{fmtV(r.area)}</td><td className={td}>{fmtV(r.center)}</td>
              <td className={td}>{fmtV(r.priority)}</td><td className="px-2 py-1 text-gray-400">{r.head_src}</td>
            </tr>
          )) : [...t.rows].sort((a, b) => a.n - b.n).map((r) => (
            <tr key={r.n} className="border-t border-gray-800">
              <td className="px-2 py-1"><PersonTag n={r.n} onPerson={onPerson} selected={selected} /></td>
              <td className={td}>{fmtV(r.af_score)}</td><td className={td}>{fmtV(r.priority)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * A stage's rules as a flowchart: asked top to bottom along a spine, a "no" carries on down, and the rule that
 * decides branches off to the right. Below it the spine turns dashed: those rules are still evaluated and shown
 * (would / wouldn't fire), but can't change anything.
 */
function Decisions({ nodes, onPerson, selected }: { nodes: TraceNode[]; onPerson: (n: number) => void; selected: number }) {
  return (
    <ol className="relative">
      {nodes.map((d, i) => {
        const next = nodes[i + 1];
        return (
          <li key={i} className={`relative pl-7 pb-3 last:pb-0 ${d.reached ? '' : 'opacity-55'}`}>
            {next && <span className={`absolute left-[9px] top-5 bottom-0 border-l ${next.reached ? 'border-gray-600' : 'border-dashed border-gray-700'}`} />}
            <Diamond d={d} />
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className={`text-sm ${d.decided ? 'text-white font-medium' : 'text-gray-200'}`}>{d.q}</span>
              <Verdict d={d} />
              {d.effect && d.result === true && (
                <span className={`text-xs ${d.decided ? 'rounded bg-blue-600/30 border border-blue-500 px-1.5 py-0.5 text-blue-100' : d.reached ? 'text-gray-300' : 'text-gray-500 italic'}`}>
                  {d.decided ? '⟶ ' : d.reached ? '→ ' : 'would: '}{d.effect}
                </span>
              )}
            </div>
            {d.rule && <div className="font-mono text-[11px] text-gray-500 [overflow-wrap:anywhere]">{d.rule}</div>}
            {d.inputs.length > 0 && (
              <div className="mt-0.5 flex flex-wrap gap-1">
                {d.inputs.map((x) => {
                  const chip = <span className="rounded bg-gray-800 px-1.5 py-0.5 text-[11px]"><span className="text-gray-500">{x.k}</span> <span className="font-mono text-gray-200">{fmtV(x.v)}</span></span>;
                  return <span key={x.k}>{x.note ? <Tip plain tip={x.note}>{chip}</Tip> : chip}</span>;
                })}
              </div>
            )}
            {d.people && d.people.length > 0 && <PeopleTests rows={d.people} onPerson={onPerson} selected={selected} />}
            {d.note && <div className="text-[11px] text-gray-500 mt-0.5">{d.note}</div>}
          </li>
        );
      })}
    </ol>
  );
}

function Diamond({ d }: { d: TraceNode }) {
  const cls = d.decided ? 'bg-blue-500 border-blue-200'
    : d.result === true ? (d.reached ? 'bg-emerald-600 border-emerald-300' : 'border-emerald-500/60 border-dashed')
      : d.result === false ? 'bg-gray-900 border-gray-500' + (d.reached ? '' : ' border-dashed')
        : 'bg-gray-900 border-gray-700 border-dashed';
  return <span className={`absolute left-[4px] top-[5px] w-[11px] h-[11px] rotate-45 border ${cls}`} />;
}

function Verdict({ d }: { d: TraceNode }) {
  if (d.result === null) return <span className="text-[11px] text-gray-500">off / n/a</span>;
  const word = d.result ? 'yes' : 'no';
  if (!d.reached) return <span className="text-[11px] text-gray-500">({d.result ? 'would fire' : "wouldn't fire"})</span>;
  return <span className={`text-[11px] font-medium ${d.result ? 'text-emerald-300' : 'text-gray-400'}`}>{word}</span>;
}

/** Per-person tests inside a rule (someone else sharp, soft person in front): which one passed which test. */
function PeopleTests({ rows, onPerson, selected }: { rows: TracePerson[]; onPerson: (n: number) => void; selected: number }) {
  return (
    <div className="mt-1 space-y-1">
      {rows.map((r) => (
        <div key={r.n} className="flex flex-wrap items-center gap-1 text-[11px]">
          <PersonTag n={r.n} onPerson={onPerson} selected={selected} />
          {r.grade !== undefined && <span className="text-gray-500">grade {fmtV(r.grade)}</span>}
          {Object.entries(r.tests ?? {}).map(([k, ok]) => (
            <span key={k} className={`rounded px-1.5 py-0.5 ${ok ? 'bg-emerald-950/60 text-emerald-300' : 'bg-red-950/50 text-red-300'}`}>{ok ? '✓' : '✗'} {k}</span>
          ))}
          <span className={r.ok ? 'text-emerald-300 font-medium' : 'text-gray-500'}>{r.ok ? '→ counts' : '→ doesn’t count'}</span>
        </div>
      ))}
    </div>
  );
}

