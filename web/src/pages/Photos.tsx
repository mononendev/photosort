import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { api, thumbUrl } from '../api/client';
import type { ImageFilters } from '../api/client';
import { TierBadge, Stars, StatusDot, LrBadge } from '../components/TierBadge';
import ImageDetail from '../components/ImageDetail';

const SUBJECTS = ['rider_action', 'rider_posed', 'group', 'crowd_spectators', 'gear_board', 'venue_scenery', 'other', 'no_people'];
const PAGE = 60;

export default function Photos() {
  const [sp, setSp] = useSearchParams();
  const filters: ImageFilters = useMemo(() => ({
    folder: sp.get('folder') ?? '',
    recursive: sp.get('recursive') !== 'false',
    tier: sp.get('tier') ? Number(sp.get('tier')) : undefined,
    keeper: sp.get('keeper') ? sp.get('keeper') === 'true' : undefined,
    subject: sp.get('subject') ?? undefined,
    status: sp.get('status') ?? undefined,
    review: sp.get('review') ? true : undefined,
    lr_rating: sp.get('lr_rating') ? Number(sp.get('lr_rating')) : undefined,
    truth_tier: sp.get('truth_tier') ? Number(sp.get('truth_tier')) : undefined,
    truth_mismatch: sp.get('truth_mismatch') ? true : undefined,
    q: sp.get('q') ?? undefined,
    sort: sp.get('sort') ?? 'path',
    offset: Number(sp.get('offset') ?? 0),
    limit: PAGE,
  }), [sp]);
  const { data, isFetching } = useQuery({ queryKey: ['images', filters], queryFn: () => api.images(filters), placeholderData: keepPreviousData, refetchInterval: 8000 });
  // `open` comes from the URL until the user navigates within the modal; `undefined` = follow the URL.
  const [openState, setOpenState] = useState<number | null | undefined>(undefined);
  const open = openState === undefined ? (sp.get('open') ? Number(sp.get('open')) : null) : openState;
  const setOpen = (v: number | null) => setOpenState(v);

  const set = (k: string, v: string | undefined) => {
    const n = new URLSearchParams(sp);
    if (v === undefined || v === '') n.delete(k); else n.set(k, v);
    if (k !== 'offset') n.delete('offset');
    n.delete('open');
    setOpenState(undefined);
    setSp(n);
  };
  const items = data?.items ?? [];
  const nav = (dir: 1 | -1) => {
    if (open === null) return;
    const i = items.findIndex((x) => x.id === open);
    const nx = items[i + dir];
    if (nx?.id) setOpen(nx.id);
  };
  const total = data?.total ?? 0;
  const offset = filters.offset ?? 0;
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1 text-sm';

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <input value={filters.folder} onChange={(e) => set('folder', e.target.value)} placeholder="folder (relative to photos root)" className={`${sel} w-72`} />
        <label className="text-xs text-gray-400 flex items-center gap-1"><input type="checkbox" checked={filters.recursive} onChange={(e) => set('recursive', e.target.checked ? undefined : 'false')} /> recursive</label>
        <select value={filters.tier ?? ''} onChange={(e) => set('tier', e.target.value)} className={sel}>
          <option value="">any focus</option><option value="2">2 · sharp</option><option value="1">1 · partial</option><option value="0">0 · nobody</option>
        </select>
        <select value={filters.keeper === undefined ? '' : String(filters.keeper)} onChange={(e) => set('keeper', e.target.value)} className={sel}>
          <option value="">keeper?</option><option value="true">keepers</option><option value="false">culls</option>
        </select>
        <select value={filters.subject ?? ''} onChange={(e) => set('subject', e.target.value)} className={sel}>
          <option value="">any subject</option>{SUBJECTS.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
        <select value={filters.status ?? ''} onChange={(e) => set('status', e.target.value)} className={sel}>
          <option value="">any status</option><option value="pending">pending</option><option value="analyzed">analyzed</option><option value="tagged">tagged</option><option value="error">error</option>
        </select>
        <label className="text-xs text-gray-400 flex items-center gap-1"><input type="checkbox" checked={!!filters.review} onChange={(e) => set('review', e.target.checked ? '1' : undefined)} /> needs review</label>
        <label className="text-xs text-gray-400 flex items-center gap-1"><input type="checkbox" checked={!!filters.truth_mismatch} onChange={(e) => set('truth_mismatch', e.target.checked ? '1' : undefined)} /> ≠ ground truth</label>
        <select value={filters.lr_rating ?? ''} onChange={(e) => set('lr_rating', e.target.value)} className={sel}>
          <option value="">any LR rating</option>{[0, 1, 2, 3, 4, 5].map((r) => <option key={r} value={r}>LR {r}★</option>)}
        </select>
        <input value={filters.q ?? ''} onChange={(e) => set('q', e.target.value)} placeholder="search keywords / name" className={`${sel} w-56`} />
        <select value={filters.sort} onChange={(e) => set('sort', e.target.value)} className={sel}>
          <option value="path">by path</option><option value="newest">newest</option><option value="score">by score</option><option value="sharpness">by sharpness</option><option value="lr">by your LR rating</option>
        </select>
        <span className="ml-auto text-xs text-gray-500">{isFetching ? 'loading…' : `${total} photos`}</span>
      </div>

      <div className="grid gap-2 grid-cols-[repeat(auto-fill,minmax(180px,1fr))]">
        {items.map((it) => (
          <button key={it.id ?? it.rel} onClick={() => it.id && setOpen(it.id)} className="group text-left rounded-lg overflow-hidden border border-gray-800 bg-gray-900 hover:border-gray-600">
            <div className="aspect-[3/2] bg-gray-950 relative">
              {it.id && it.status !== 'pending' ? <img src={thumbUrl(it.id)} alt="" loading="lazy" className="w-full h-full object-cover" /> : <div className="w-full h-full flex items-center justify-center text-gray-700 text-xs">pending</div>}
              <div className="absolute top-1 left-1 flex gap-1"><TierBadge tier={it.focus_tier} small />{it.truth_tier !== null && it.truth_tier !== undefined && <span className={`rounded px-1 text-[10px] ${it.truth_tier === it.focus_tier ? 'bg-emerald-900/80 text-emerald-200' : 'bg-red-900/80 text-red-200'}`} title="your ground truth">T{it.truth_tier}</span>}{it.review && <span className="rounded bg-amber-900/80 text-amber-200 px-1 text-[10px]">review</span>}{it.overridden && <span className="rounded bg-purple-900/80 text-purple-200 px-1 text-[10px]">edited</span>}</div>
              {it.keeper === false && <div className="absolute inset-0 bg-black/40" />}
            </div>
            <div className="px-2 py-1.5 text-xs">
              <div className="flex items-center gap-1"><StatusDot status={it.status} /><span className="truncate text-gray-300">{it.name}</span></div>
              <div className="flex items-center justify-between mt-0.5"><span className="text-gray-500 truncate">{it.subject !== 'unknown' ? it.subject : ''}</span><Stars n={it.quality_score} /></div>
              <div className="flex items-center justify-end mt-0.5"><LrBadge rating={it.lr_rating} label={it.lr_label} /></div>
            </div>
          </button>
        ))}
      </div>
      {total > PAGE && (
        <div className="flex items-center justify-center gap-3 text-sm">
          <button disabled={offset === 0} onClick={() => set('offset', String(Math.max(0, offset - PAGE)))} className="px-3 py-1 rounded bg-gray-800 disabled:opacity-40">← prev</button>
          <span className="text-gray-500">{offset + 1}–{Math.min(offset + PAGE, total)} of {total}</span>
          <button disabled={offset + PAGE >= total} onClick={() => set('offset', String(offset + PAGE))} className="px-3 py-1 rounded bg-gray-800 disabled:opacity-40">next →</button>
        </div>
      )}
      {open !== null && <ImageDetail id={open} onClose={() => setOpen(null)} onNav={nav} />}
    </div>
  );
}
