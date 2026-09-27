import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { api, thumbUrl, COMPOSITIONS, GROUPS, RATINGS, SUBJECTS, TIER_LABEL, TIERS } from '../api/client';
import type { ImageFilters } from '../api/client';
import ImageDetail from '../components/ImageDetail';
import Loading from '../components/Loading';
import SegButton from '../components/SegButton';
import { RatingBadge, TierBadge } from '../components/TierBadge';
import useHotkeys from '../hooks/useHotkeys';

// Which photos the queue starts from: the needs-review flag, just its metrics-split half, or everything.
// Ctrl/⌘-click on metrics split (or clicking it again) flips it to the needs-review photos *without* a metrics split.
const QUEUES = [['', 'needs review'], ['split', 'metrics split'], ['all', 'all']] as const;
const PAGE = 500;   // the API's per-request cap; stepping past either end loads the neighbouring page

// URL params that go to the API as they are, by type. The queue/rated/folder/... ones above them are mapped by hand.
const NUM_KEYS = ['local_tier', 'vlm_tier', 'rating', 'group', 'lr_rating', 'truth_tier', 'people_min', 'people_max', 'score_min', 'score_max',
  'eye_min', 'eye_max', 'iso_min', 'iso_max', 'f_min', 'f_max', 'focal_min', 'focal_max'] as const;
const BOOL_KEYS = ['keeper', 'stale', 'lifted', 'overridden', 'noted', 'truth_mismatch'] as const;
const STR_KEYS = ['stages', 'composition', 'eye_src', 'primary_by', 'camera', 'lens', 'lr_label', 'taken_from', 'taken_to'] as const;

const SORTS = [
  ['path', 'by path'], ['name', 'by file name'], ['newest', 'newest added'], ['taken', 'capture time, oldest first'],
  ['taken_desc', 'capture time, newest first'], ['score', 'best score first'], ['score_low', 'worst score first'],
  ['eye_sharpness', 'sharpest eyes first'], ['eye_softest', 'softest eyes first'], ['sharpness', 'sharpest head first'],
  ['people', 'most people first'], ['iso', 'highest ISO first'], ['lr', 'sidecar rating'], ['rated', 'rated most recently first'],
  ['shuffle', 'shuffled'],
] as const;
const tierOpts = [...TIERS].reverse().map((t) => [String(t), `${t} · ${TIER_LABEL[t]}`] as [string, string]);

/** A shutter speed as typed ("1/500", "1/500s", "0.5", "2s") in seconds, or NaN. */
function seconds(s: string): number {
  const m = s.trim().match(/^(\d*\.?\d+)\s*\/\s*(\d*\.?\d+)\s*s?$/);
  return m ? Number(m[1]) / Number(m[2]) : Number(s.trim().replace(/s$/, ''));
}
const fmtShutter = (s: number) => (s >= 1 ? `${+s.toFixed(1)}s` : `1/${Math.round(1 / s)}`);
const exifDate = (d: string | null) => d?.slice(0, 10).replaceAll(':', '-');

/** Text field that writes back once typing pauses, so the URL (and the query) don't change on every keystroke. */
function DraftInput({ value, onCommit, className, placeholder, inputMode }: {
  value: string; onCommit: (v: string) => void; className: string; placeholder: string; inputMode?: 'decimal' | 'numeric';
}) {
  const [draft, setDraft] = useState<string | null>(null);
  useEffect(() => {
    if (draft === null) return;
    const t = setTimeout(() => { onCommit(draft); setDraft(null); }, 400);
    return () => clearTimeout(t);
  }, [draft, onCommit]);
  return <input value={draft ?? value} onChange={(e) => setDraft(e.target.value)} placeholder={placeholder} inputMode={inputMode} className={className} />;
}

/** A collapsible group of filters, with how many of its filters are set. */
function Section({ title, n, open, onToggle, children }: { title: string; n: number; open: boolean; onToggle: () => void; children: ReactNode }) {
  return (
    <div className="border-b border-gray-800 last:border-0 pb-2">
      <button onClick={onToggle} aria-expanded={open} className="w-full flex items-center gap-2 py-1 text-xs uppercase tracking-wider text-gray-400 hover:text-white">
        <svg viewBox="0 0 12 12" className={`w-2.5 h-2.5 transition-transform ${open ? 'rotate-90' : ''}`} fill="currentColor"><path d="M4 2l5 4-5 4z" /></svg>
        {title}
        {n > 0 && <span className="ml-auto rounded-full bg-blue-600 text-white text-[10px] px-1.5 normal-case tracking-normal">{n}</span>}
      </button>
      {open && <div className="space-y-2 pt-1 animate-[menu-in_120ms_ease-out]">{children}</div>}
    </div>
  );
}

/**
 * Needs-review photos you haven't rated yet, one at a time in the detail view; rating one drops it from the queue.
 * The filter menu narrows it to a folder (and more), or widens it to rated and not-flagged photos.
 */
export default function Review() {
  const [sp, setSp] = useSearchParams();
  const filters: ImageFilters = useMemo(() => {
    const f: Record<string, unknown> = {
      review: !sp.get('queue') || sp.get('queue') === 'nosplit' || undefined,
      split: sp.get('queue') === 'split' ? true : sp.get('queue') === 'nosplit' ? false : undefined,
      // asking for one of your ratings means showing rated photos
      reviewed: sp.get('rated') === 'all' || sp.get('rating') ? undefined : false,
      folder: sp.get('folder') ?? '',
      recursive: sp.get('recursive') !== 'false',
      tier: sp.get('tier') ? Number(sp.get('tier')) : undefined,
      subject: sp.get('subject') ?? undefined,
      status: sp.get('status') ?? undefined,
      q: sp.get('q') ?? undefined,
      sort: sp.get('sort') ?? 'path',
    };
    for (const k of NUM_KEYS) { const v = Number(sp.get(k) ?? ''); if (sp.get(k) && Number.isFinite(v)) f[k] = v; }
    for (const k of BOOL_KEYS) if (sp.get(k)) f[k] = sp.get(k) === 'true';
    for (const k of STR_KEYS) if (sp.get(k)) f[k] = sp.get(k);
    for (const k of ['shutter_min', 'shutter_max']) { const v = seconds(sp.get(k) ?? ''); if (sp.get(k) && Number.isFinite(v)) f[k] = v; }
    return f as ImageFilters;
  }, [sp]);
  const [offset, setOffset] = useState(0);
  // stay: a photo reached through the history, shown even when it's no longer in the queue (you've rated it).
  const [picked, setPicked] = useState<{ id: number; stay?: boolean } | { edge: 'first' | 'last' } | null>(null);
  // The photos you've stepped away from (back) and backed out of (fwd), so ← undoes a rating's advance.
  const [hist, setHist] = useState<{ back: number[]; fwd: number[] }>({ back: [], fwd: [] });
  const { data, isLoading, isFetching, isError, error } = useQuery({
    queryKey: ['images', 'review-queue', filters, offset],
    queryFn: () => api.images({ ...filters, offset, limit: PAGE }),
    placeholderData: keepPreviousData,
  });
  const set = useCallback((k: string, v: string | undefined) => {
    setSp((cur) => { const n = new URLSearchParams(cur); if (v === undefined || v === '') n.delete(k); else n.set(k, v); return n; }, { replace: true });
    setOffset(0); setPicked(null); setHist({ back: [], fwd: [] });
  }, [setSp]);
  const clear = useCallback((keys: readonly string[]) => {
    setSp((cur) => { const n = new URLSearchParams(cur); keys.forEach((k) => n.delete(k)); return n; }, { replace: true });
    setOffset(0); setPicked(null); setHist({ back: [], fwd: [] });
  }, [setSp]);

  const items = useMemo(() => (data?.items ?? []).filter((x) => x.id !== null), [data]);
  const total = data?.total ?? 0;
  // Follow the pick while it's still in the queue; once it's rated and gone, fall back to the first one left.
  const found = picked && 'id' in picked ? items.findIndex((x) => x.id === picked.id) : -1;
  const i = found >= 0 ? found : picked && 'edge' in picked && picked.edge === 'last' ? items.length - 1 : 0;
  const away = found < 0 && picked && 'id' in picked && picked.stay ? picked.id : null;   // revisited, out of the queue
  const id = away ?? items[i]?.id ?? null;
  // ← and → walk the history first (back to the photo you just rated, then forward again), and the queue after that.
  // Past the start of this visit's history, ← carries on through what you rated before, newest first, from the server.
  const seeding = useRef(false);
  const nav = useCallback((dir: 1 | -1) => {
    if (seeding.current) return;
    const [from, to] = dir === -1 ? [hist.back, hist.fwd] : [hist.fwd, hist.back];
    if (from.length && id !== null) {
      const prev = from[from.length - 1];
      const moved = { from: from.slice(0, -1), to: [...to, id] };
      setHist(dir === -1 ? { back: moved.from, fwd: moved.to } : { back: moved.to, fwd: moved.from });
      setPicked({ id: prev, stay: true });
      return;
    }
    const step = (p: NonNullable<typeof picked>) => {
      if (id !== null) setHist({ back: dir === 1 ? [...hist.back, id] : hist.back, fwd: [] });
      setPicked(p);
    };
    const inQueue = () => {
      const nx = away === null ? items[i + dir] : items[dir === 1 ? i : i - 1];
      if (nx?.id) step({ id: nx.id });
      else if (dir === 1 && offset + items.length < total) { setOffset(offset + PAGE); step({ edge: 'first' }); }
      else if (dir === -1 && offset > 0) { setOffset(Math.max(0, offset - PAGE)); step({ edge: 'last' }); }
    };
    if (dir === 1 || id === null) return inQueue();
    seeding.current = true;
    const skip = new Set([id, ...hist.fwd]);
    api.images({ ...filters, reviewed: true, sort: 'rated', offset: 0, limit: Math.min(PAGE, skip.size + 50) })
      .then((r) => r.items.flatMap((x) => (x.id !== null && !skip.has(x.id) ? [x.id] : [])).reverse(), () => [])
      .then((back) => {
        seeding.current = false;
        if (!back.length) return inQueue();
        setHist({ back: back.slice(0, -1), fwd: [...hist.fwd, id] });
        setPicked({ id: back[back.length - 1], stay: true });
      });
  }, [hist, id, away, items, i, offset, total, filters]);

  const [menu, setMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  // The panel hangs from the viewport's right edge just under the button, so it never runs off either side.
  const [menuTop, setMenuTop] = useState(0);
  useLayoutEffect(() => {
    if (menu) setMenuTop((menuRef.current?.getBoundingClientRect().bottom ?? 0) + 4);
  }, [menu]);
  useEffect(() => {
    if (!menu) return;
    const close = (e: PointerEvent) => { if (!menuRef.current?.contains(e.target as Node)) setMenu(false); };
    window.addEventListener('pointerdown', close);
    return () => window.removeEventListener('pointerdown', close);
  }, [menu]);
  useEffect(() => {   // opening the menu scrolls the thumbnail strip to the photo you're on
    if (menu) menuRef.current?.querySelector('[data-current]')?.scrollIntoView({ block: 'nearest' });
  }, [menu]);
  useHotkeys({ '/': () => setMenu((m) => !m) });   // f is group 4
  const { data: facets } = useQuery({ queryKey: ['image-facets'], queryFn: api.imageFacets, enabled: menu, staleTime: 60_000 });
  const nActive = new Set(sp.keys()).size;

  const sel = 'w-full min-w-0 bg-gray-900 border border-gray-700 rounded px-2 py-1.5 sm:py-1 text-sm';
  const check = 'flex items-center gap-2 text-sm text-gray-300 py-0.5';
  const lbl = 'text-xs text-gray-500';
  // Render helpers, called as functions (not components) so a DraftInput keeps its draft across renders.
  const pick = (k: string, any: string, opts: readonly (readonly [string, string])[]) => (
    <select value={sp.get(k) ?? ''} onChange={(e) => set(k, e.target.value)} className={sel}>
      <option value="">{any}</option>{opts.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
    </select>
  );
  const facet = (k: string, any: string, list?: { value: string; n: number }[], label = (v: string) => v) => {
    const cur = sp.get(k);   // keep a value from the URL selectable even when this library has none of it
    const opts = (list ?? []).map((x) => [x.value, `${label(x.value)} (${x.n})`] as [string, string]);
    return pick(k, any, cur && !opts.some(([v]) => v === cur) ? [[cur, label(cur)], ...opts] : opts);
  };
  const tri = (k: string, label: string, yes = 'yes', no = 'no') => (
    <div className="flex items-center gap-1">
      <span className="text-xs text-gray-400 flex-1 min-w-0">{label}</span>
      {([['', 'any'], ['true', yes], ['false', no]] as const).map(([v, l]) => (
        <SegButton key={v} on={(sp.get(k) ?? '') === v} onClick={() => set(k, v)} className="text-xs px-2 py-1 sm:py-0.5 whitespace-nowrap">{l}</SegButton>
      ))}
    </div>
  );
  const range = (label: string, lo: string, hi: string, span?: [number | string | null, number | string | null], show = (v: number) => String(+v.toFixed(2))) => {
    const ph = (v: number | string | null | undefined, d: string) => (v === null || v === undefined ? d : typeof v === 'number' ? show(v) : v);
    return (
      <div>
        <div className={lbl}>{label}</div>
        <div className="flex items-center gap-1">
          <DraftInput key={lo} value={sp.get(lo) ?? ''} onCommit={(v) => set(lo, v.trim())} placeholder={ph(span?.[0], 'min')} inputMode="decimal" className={sel} />
          <span className="text-gray-600">–</span>
          <DraftInput key={hi} value={sp.get(hi) ?? ''} onCommit={(v) => set(hi, v.trim())} placeholder={ph(span?.[1], 'max')} inputMode="decimal" className={sel} />
        </div>
      </div>
    );
  };
  const rg = facets?.ranges;

  const SECTIONS: { key: string; title: string; keys: readonly string[]; body: () => ReactNode }[] = [
    {
      key: 'queue', title: 'Queue', keys: ['queue', 'rated', 'folder', 'recursive', 'q'], body: () => <>
        <div className="flex gap-1">
          {QUEUES.map(([v, label]) => {
            const cur = sp.get('queue') ?? '';
            if (v !== 'split') return <SegButton key={v} on={cur === v} onClick={() => set('queue', v)} className="flex-1 whitespace-nowrap text-xs px-1 py-1.5 sm:py-1">{label}</SegButton>;
            const not = cur === 'nosplit';
            return (
              <SegButton key={v} on={cur === 'split' || not} title="ctrl/⌘-click (or click again): needs review, minus the metrics-split photos"
                onClick={(e) => set('queue', e.ctrlKey || e.metaKey || cur === 'split' ? 'nosplit' : 'split')}
                className="flex-1 whitespace-nowrap text-xs px-1 py-1.5 sm:py-1">{not ? 'review − split' : label}</SegButton>
            );
          })}
        </div>
        <label className={check}><input type="checkbox" checked={filters.reviewed === false} disabled={!!sp.get('rating')} onChange={(e) => set('rated', e.target.checked ? undefined : 'all')} /> hide photos you've rated</label>
        <DraftInput value={filters.folder ?? ''} onCommit={(v) => set('folder', v)} placeholder="folder (relative to photos root)" className={sel} />
        <label className={check}><input type="checkbox" checked={filters.recursive} onChange={(e) => set('recursive', e.target.checked ? undefined : 'false')} /> include subfolders</label>
        <DraftInput value={filters.q ?? ''} onCommit={(v) => set('q', v)} placeholder="search name, keywords, description, notes" className={sel} />
      </>,
    },
    {
      key: 'focus', title: 'Focus', keys: ['tier', 'local_tier', 'vlm_tier', 'stages', 'stale', 'eye_src', 'primary_by', 'eye_min', 'eye_max', 'lifted'], body: () => <>
        {pick('tier', 'any final focus', tierOpts)}
        <div className="grid grid-cols-2 gap-1">{pick('local_tier', 'any local tier', tierOpts)}{pick('vlm_tier', 'any model tier', tierOpts)}</div>
        {pick('stages', 'local and model: either', [['disagree', 'local and model disagree'], ['agree', 'local and model agree']])}
        {tri('stale', 'model saw an older exposure', 'stale', 'current')}
        {pick('eye_src', 'eyes found any way', [['face', 'eyes from face landmarks'], ['pose', 'eyes from pose keypoints'], ['none', 'eyes not located']])}
        {pick('primary_by', 'subject picked any way', [['af', 'subject picked by camera AF'], ['priority', 'subject picked by prominence']])}
        {range('eye sharpness', 'eye_min', 'eye_max', rg?.eye)}
        {tri('lifted', 'underexposed, brightened', 'lifted', 'not')}
      </>,
    },
    {
      key: 'content', title: 'Content', keys: ['subject', 'composition', 'people_min', 'people_max', 'score_min', 'score_max', 'keeper', 'status'], body: () => <>
        {facets ? facet('subject', 'any subject', facets.subjects) : pick('subject', 'any subject', SUBJECTS.map((s) => [s, s]))}
        {facets ? facet('composition', 'any composition', facets.compositions) : pick('composition', 'any composition', COMPOSITIONS.map((s) => [s, s]))}
        <div className="grid grid-cols-2 gap-2">
          {range('people', 'people_min', 'people_max', rg?.people, String)}
          {range('quality score', 'score_min', 'score_max', rg?.score, String)}
        </div>
        {tri('keeper', 'keeper', 'keeper', 'not')}
        {pick('status', 'any status', [['analyzed', 'analyzed'], ['skipped', 'skipped'], ['tagged', 'tagged'], ['pending', 'pending'], ['error', 'error']])}
      </>,
    },
    {
      key: 'camera', title: 'Camera', keys: ['camera', 'lens', 'iso_min', 'iso_max', 'f_min', 'f_max', 'shutter_min', 'shutter_max', 'focal_min', 'focal_max', 'taken_from', 'taken_to'], body: () => <>
        {facet('camera', 'any camera', facets?.cameras)}
        {facet('lens', 'any lens', facets?.lenses)}
        <div className="grid grid-cols-2 gap-2">
          {range('ISO', 'iso_min', 'iso_max', rg?.iso, String)}
          {range('aperture f/', 'f_min', 'f_max', rg?.f, (v) => String(+v.toFixed(1)))}
          {range('shutter (1/500, 2s)', 'shutter_min', 'shutter_max', rg?.shutter, fmtShutter)}
          {range('focal (35mm eq.)', 'focal_min', 'focal_max', rg?.focal, (v) => `${Math.round(v)}`)}
        </div>
        <div>
          <div className={lbl}>taken between</div>
          <div className="flex items-center gap-1">
            <input type="date" value={sp.get('taken_from') ?? ''} min={exifDate(rg?.taken[0] ?? null)} max={exifDate(rg?.taken[1] ?? null)} onChange={(e) => set('taken_from', e.target.value)} className={sel} />
            <span className="text-gray-600">–</span>
            <input type="date" value={sp.get('taken_to') ?? ''} min={exifDate(rg?.taken[0] ?? null)} max={exifDate(rg?.taken[1] ?? null)} onChange={(e) => set('taken_to', e.target.value)} className={sel} />
          </div>
        </div>
      </>,
    },
    {
      key: 'yours', title: 'Your calls', keys: ['rating', 'group', 'overridden', 'noted', 'lr_rating', 'lr_label', 'truth_tier', 'truth_mismatch'], body: () => <>
        {pick('rating', 'any rating of yours', RATINGS.map((r) => [String(r.value), `${r.short} · ${r.label}`]))}
        {pick('group', 'any group', [...GROUPS.map((g) => [String(g.value), `group ${g.value} (${g.key})`] as [string, string]), ['0', 'in no group']])}
        {tri('overridden', 'changed anything', 'yes', 'no')}
        {tri('noted', 'has a note', 'yes', 'no')}
        <div className="grid grid-cols-2 gap-1">
          {pick('lr_rating', 'any sidecar ★', [0, 1, 2, 3, 4, 5].map((n) => [String(n), n ? '★'.repeat(n) : 'unrated']))}
          {facet('lr_label', 'any sidecar label', facets?.lr_labels)}
        </div>
        {pick('truth_tier', 'any imported truth', tierOpts)}
        <label className={check}><input type="checkbox" checked={sp.get('truth_mismatch') === 'true'} onChange={(e) => set('truth_mismatch', e.target.checked ? 'true' : undefined)} /> truth disagrees with the final tier</label>
      </>,
    },
  ];
  const [open, setOpen] = useState<Set<string>>(() => new Set(['queue', ...SECTIONS.filter((s) => s.keys.some((k) => sp.get(k))).map((s) => s.key)]));
  const toggle = (k: string) => setOpen((o) => { const n = new Set(o); if (!n.delete(k)) n.add(k); return n; });

  const toolbar = (
    <>
      <span className="text-xs text-gray-500 tabular-nums whitespace-nowrap flex items-center gap-1.5">
        {isFetching && <span className="inline-block w-3 h-3 rounded-full border-2 border-blue-400 border-t-transparent animate-spin" aria-label="updating" />}
        {id === null ? 0 : away !== null ? '–' : offset + i + 1} / {total}
      </span>
      <div ref={menuRef} className="relative" onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); setMenu(false); } }}>
        <button onClick={() => setMenu(!menu)} aria-label="Filters" aria-expanded={menu} title="Filters (/)"
          className={`relative px-3 py-1.5 sm:px-2 sm:py-0.5 rounded hover:text-white active:bg-gray-800 ${menu || nActive ? 'text-blue-300' : 'text-gray-400'}`}>
          <svg viewBox="0 0 24 24" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round"><path d="M4 6h16M7 12h10M10 18h4" /></svg>
          {nActive > 0 && <span className="absolute -top-0.5 -right-0.5 min-w-3.5 h-3.5 rounded-full bg-blue-600 text-white text-[9px] leading-3.5 text-center">{nActive}</span>}
        </button>
        {menu && (
          <div style={{ top: menuTop, maxHeight: `calc(100dvh - ${menuTop}px - 0.75rem)` }}
            className="fixed right-3 z-30 w-[min(60rem,calc(100vw-1.5rem))] rounded-lg border border-gray-700 bg-gray-900 shadow-xl p-3 flex flex-col md:flex-row gap-3 overflow-hidden animate-[menu-in_120ms_ease-out]">
            <div className="md:w-80 shrink-0 max-h-[45vh] md:max-h-none overflow-y-auto overscroll-contain pr-1 space-y-1">
              {SECTIONS.map((s) => {
                const n = s.keys.filter((k) => sp.get(k)).length;
                return (
                  <Section key={s.key} title={s.title} n={n} open={open.has(s.key)} onToggle={() => toggle(s.key)}>
                    {s.body()}
                    {n > 0 && <button onClick={() => clear(s.keys)} className="text-xs text-gray-500 hover:text-white">clear {s.title.toLowerCase()}</button>}
                  </Section>
                );
              })}
              <div className="pt-1 space-y-2">
                <div className={lbl}>sort</div>
                <select value={filters.sort} onChange={(e) => set('sort', e.target.value === 'path' ? undefined : e.target.value)} className={sel}>
                  {SORTS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                </select>
                {nActive > 0 && <button onClick={() => { setSp({}, { replace: true }); setOffset(0); setPicked(null); setHist({ back: [], fwd: [] }); }} className="text-xs text-gray-400 hover:text-white">reset everything to defaults</button>}
              </div>
            </div>
            {/* This page of the queue: click one to jump to it. */}
            <div className="flex-1 min-w-0 flex flex-col gap-1">
              <div className="text-xs text-gray-500 tabular-nums">{isFetching ? 'updating…' : `${total} photo${total === 1 ? '' : 's'} match`}</div>
              <div className={`min-h-0 flex-1 overflow-y-auto overscroll-contain grid grid-cols-[repeat(auto-fill,minmax(88px,1fr))] gap-1 content-start transition-opacity ${isFetching ? 'opacity-50' : ''}`}>
                {items.map((it, j) => (
                  <button key={it.id} data-current={(away === null && j === i) || undefined}
                    onClick={() => { if (id !== null && it.id !== id) setHist({ back: [...hist.back, id], fwd: [] }); setPicked({ id: it.id! }); setMenu(false); }} title={it.rel}
                    className={`relative aspect-[3/2] rounded overflow-hidden bg-gray-950 border-2 ${away === null && j === i ? 'border-blue-400' : 'border-transparent hover:border-gray-500'}`}>
                    <img src={thumbUrl(it.id!)} alt="" loading="lazy" className={`w-full h-full object-cover ${it.reviewed && (away !== null || j !== i) ? 'opacity-50' : ''}`} />
                    <span className="absolute top-0.5 left-0.5 flex gap-0.5"><TierBadge tier={it.focus_tier} small />{it.reviewed && <RatingBadge rating={it.rating} />}</span>
                  </button>
                ))}
                {!items.length && !isFetching && <div className="col-span-full py-10 text-center text-sm text-gray-500">No photos match.</div>}
              </div>
            </div>
          </div>
        )}
      </div>
    </>
  );

  if (isLoading) return <Loading label="loading the review queue…" className="min-h-[calc(100dvh-8rem)]" />;
  if (isError && !data) return <div className="min-h-[calc(100dvh-8rem)] flex items-center justify-center text-sm text-red-400">couldn't load the queue: {String((error as Error)?.message ?? error)}</div>;
  if (id === null) return (
    <div className="min-h-[calc(100dvh-8rem)] flex flex-col">
      {/* the menu opens down and to the left of its button, so the button sits at the right */}
      <div className="flex items-center justify-end gap-2 px-3 sm:px-4 py-2 border-b border-gray-800">{toolbar}</div>
      <div className="flex-1 flex flex-col items-center justify-center gap-2 text-sm text-gray-500 animate-[fade-in_300ms_ease-out]">
        {nActive ? 'Nothing matches these filters.' : 'Nothing left to review.'}
        {nActive > 0 && <button onClick={() => setMenu(true)} className="text-xs text-blue-300 hover:text-white">adjust filters (/)</button>}
      </div>
    </div>
  );
  return <ImageDetail id={id} onNav={nav} toolbar={toolbar} />;
}
