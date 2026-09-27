import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { api, thumbUrl, SUBJECTS, TIER_LABEL, TIERS } from '../api/client';
import type { ImageFilters } from '../api/client';
import ImageDetail from '../components/ImageDetail';
import SegButton from '../components/SegButton';
import { RatingBadge, TierBadge } from '../components/TierBadge';
import useHotkeys from '../hooks/useHotkeys';

// Which photos the queue starts from: the needs-review flag, just its metrics-split half, or everything.
// Ctrl/⌘-click on metrics split (or clicking it again) flips it to the needs-review photos *without* a metrics split.
const QUEUES = [['', 'needs review'], ['split', 'metrics split'], ['all', 'all']] as const;
const PAGE = 500;   // the API's per-request cap; stepping past either end loads the neighbouring page

/** Text field that writes back once typing pauses, so the URL (and the query) don't change on every keystroke. */
function DraftInput({ value, onCommit, className, placeholder }: { value: string; onCommit: (v: string) => void; className: string; placeholder: string }) {
  const [draft, setDraft] = useState<string | null>(null);
  useEffect(() => {
    if (draft === null) return;
    const t = setTimeout(() => { onCommit(draft); setDraft(null); }, 400);
    return () => clearTimeout(t);
  }, [draft, onCommit]);
  return <input value={draft ?? value} onChange={(e) => setDraft(e.target.value)} placeholder={placeholder} className={className} />;
}

/**
 * Needs-review photos you haven't rated yet, one at a time in the detail view; rating one drops it from the queue.
 * The filter menu narrows it to a folder (and more), or widens it to rated and not-flagged photos.
 */
export default function Review() {
  const [sp, setSp] = useSearchParams();
  const filters: ImageFilters = useMemo(() => ({
    review: !sp.get('queue') || sp.get('queue') === 'nosplit' || undefined,
    split: sp.get('queue') === 'split' ? true : sp.get('queue') === 'nosplit' ? false : undefined,
    reviewed: sp.get('rated') === 'all' ? undefined : false,
    folder: sp.get('folder') ?? '',
    recursive: sp.get('recursive') !== 'false',
    tier: sp.get('tier') ? Number(sp.get('tier')) : undefined,
    subject: sp.get('subject') ?? undefined,
    status: sp.get('status') ?? undefined,
    q: sp.get('q') ?? undefined,
    sort: sp.get('sort') ?? 'path',
  }), [sp]);
  const [offset, setOffset] = useState(0);
  const [picked, setPicked] = useState<{ id: number } | { edge: 'first' | 'last' } | null>(null);
  const { data, isLoading } = useQuery({
    queryKey: ['images', 'review-queue', filters, offset],
    queryFn: () => api.images({ ...filters, offset, limit: PAGE }),
    placeholderData: keepPreviousData,
  });
  const set = useCallback((k: string, v: string | undefined) => {
    setSp((cur) => { const n = new URLSearchParams(cur); if (v === undefined || v === '') n.delete(k); else n.set(k, v); return n; }, { replace: true });
    setOffset(0); setPicked(null);
  }, [setSp]);

  const items = useMemo(() => (data?.items ?? []).filter((x) => x.id !== null), [data]);
  const total = data?.total ?? 0;
  // Follow the pick while it's still in the queue; once it's rated and gone, fall back to the first one left.
  const found = picked && 'id' in picked ? items.findIndex((x) => x.id === picked.id) : -1;
  const i = found >= 0 ? found : picked && 'edge' in picked && picked.edge === 'last' ? items.length - 1 : 0;
  const id = items[i]?.id ?? null;
  const nav = useCallback((dir: 1 | -1) => {
    const nx = items[i + dir];
    if (nx?.id) setPicked({ id: nx.id });
    else if (dir === 1 && offset + items.length < total) { setOffset(offset + PAGE); setPicked({ edge: 'first' }); }
    else if (dir === -1 && offset > 0) { setOffset(Math.max(0, offset - PAGE)); setPicked({ edge: 'last' }); }
  }, [items, i, offset, total]);

  const [menu, setMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menu) return;
    const close = (e: PointerEvent) => { if (!menuRef.current?.contains(e.target as Node)) setMenu(false); };
    window.addEventListener('pointerdown', close);
    return () => window.removeEventListener('pointerdown', close);
  }, [menu]);
  useEffect(() => {   // opening the menu scrolls the thumbnail strip to the photo you're on
    if (menu) menuRef.current?.querySelector('[data-current]')?.scrollIntoView({ block: 'nearest' });
  }, [menu]);
  useHotkeys({ f: () => setMenu((m) => !m) });
  const nActive = ['queue', 'rated', 'folder', 'recursive', 'tier', 'subject', 'status', 'q', 'sort'].filter((k) => sp.get(k)).length;

  const sel = 'w-full bg-gray-900 border border-gray-700 rounded px-2 py-1.5 sm:py-1 text-sm';
  const check = 'flex items-center gap-2 text-sm text-gray-300 py-0.5';
  const toolbar = (
    <>
      <span className="text-xs text-gray-500 tabular-nums whitespace-nowrap">{id === null ? 0 : offset + i + 1} / {total}</span>
      <div ref={menuRef} className="relative" onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); setMenu(false); } }}>
        <button onClick={() => setMenu(!menu)} aria-label="Filters" aria-expanded={menu} title="Filters (f)"
          className={`relative px-3 py-1.5 sm:px-2 sm:py-0.5 rounded hover:text-white active:bg-gray-800 ${menu || nActive ? 'text-blue-300' : 'text-gray-400'}`}>
          <svg viewBox="0 0 24 24" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round"><path d="M4 6h16M7 12h10M10 18h4" /></svg>
          {nActive > 0 && <span className="absolute -top-0.5 -right-0.5 min-w-3.5 h-3.5 rounded-full bg-blue-600 text-white text-[9px] leading-3.5 text-center">{nActive}</span>}
        </button>
        {menu && (
          <div className="absolute right-0 top-full mt-1 z-20 w-[min(56rem,calc(100vw-1.5rem))] rounded-lg border border-gray-700 bg-gray-900 shadow-xl p-3 flex flex-col md:flex-row gap-3 animate-[menu-in_120ms_ease-out]">
            <div className="md:w-64 shrink-0 space-y-2">
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
            <label className={check}><input type="checkbox" checked={filters.reviewed === false} onChange={(e) => set('rated', e.target.checked ? undefined : 'all')} /> hide photos you've rated</label>
            <DraftInput value={filters.folder ?? ''} onCommit={(v) => set('folder', v)} placeholder="folder (relative to photos root)" className={sel} />
            <label className={check}><input type="checkbox" checked={filters.recursive} onChange={(e) => set('recursive', e.target.checked ? undefined : 'false')} /> include subfolders</label>
            <select value={filters.tier ?? ''} onChange={(e) => set('tier', e.target.value)} className={sel}>
              <option value="">any focus</option>{[...TIERS].reverse().map((t) => <option key={t} value={t}>{t} · {TIER_LABEL[t]}</option>)}
            </select>
            <select value={filters.subject ?? ''} onChange={(e) => set('subject', e.target.value)} className={sel}>
              <option value="">any subject</option>{SUBJECTS.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            <select value={filters.status ?? ''} onChange={(e) => set('status', e.target.value)} className={sel}>
              <option value="">any status</option><option value="analyzed">analyzed</option><option value="skipped">skipped</option><option value="tagged">tagged</option><option value="error">error</option>
            </select>
            <DraftInput value={filters.q ?? ''} onCommit={(v) => set('q', v)} placeholder="search keywords / name" className={sel} />
            <select value={filters.sort} onChange={(e) => set('sort', e.target.value === 'path' ? undefined : e.target.value)} className={sel}>
              <option value="path">by path</option><option value="newest">newest</option><option value="score">by score</option><option value="eye_sharpness">by eye sharpness</option><option value="sharpness">by head sharpness</option>
            </select>
            {nActive > 0 && <button onClick={() => { setSp({}, { replace: true }); setOffset(0); setPicked(null); }} className="text-xs text-gray-400 hover:text-white">reset to defaults</button>}
            </div>
            {/* This page of the queue: click one to jump to it. */}
            <div className="flex-1 min-w-0 max-h-[40vh] md:max-h-[70vh] overflow-y-auto overscroll-contain grid grid-cols-[repeat(auto-fill,minmax(88px,1fr))] gap-1 content-start">
              {items.map((it, j) => (
                <button key={it.id} data-current={j === i || undefined}
                  onClick={() => { setPicked({ id: it.id! }); setMenu(false); }} title={it.rel}
                  className={`relative aspect-[3/2] rounded overflow-hidden bg-gray-950 border-2 ${j === i ? 'border-blue-400' : 'border-transparent hover:border-gray-500'}`}>
                  <img src={thumbUrl(it.id!)} alt="" loading="lazy" className={`w-full h-full object-cover ${it.reviewed && j !== i ? 'opacity-50' : ''}`} />
                  <span className="absolute top-0.5 left-0.5 flex gap-0.5"><TierBadge tier={it.focus_tier} small />{it.reviewed && <RatingBadge rating={it.rating} />}</span>
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
    </>
  );

  if (isLoading) return <p className="p-4 text-sm text-gray-500">loading…</p>;
  if (id === null) return (
    <div className="p-4 flex items-center gap-3 text-sm text-gray-500">
      {nActive ? 'Nothing matches these filters.' : 'Nothing left to review.'}
      <div className="flex items-center">{toolbar}</div>
    </div>
  );
  return <ImageDetail id={id} onNav={nav} toolbar={toolbar} />;
}
