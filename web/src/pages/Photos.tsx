import { useCallback, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { api, thumbUrl } from '../api/client';
import type { ImageFilters } from '../api/client';
import { TierBadge, Stars, StatusDot, LrBadge, RatingBadge } from '../components/TierBadge';
import ImageDetail from '../components/ImageDetail';
import Tip from '../components/Tip';
import { useBusy } from '../hooks/useJobs';

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
    reviewed: sp.get('reviewed') ? sp.get('reviewed') === 'true' : undefined,
    rating: sp.get('rating') ? Number(sp.get('rating')) : undefined,
    q: sp.get('q') ?? undefined,
    sort: sp.get('sort') ?? 'path',
    offset: Number(sp.get('offset') ?? 0),
    limit: PAGE,
  }), [sp]);
  const busy = useBusy();
  const { data, isFetching } = useQuery({ queryKey: ['images', filters], queryFn: () => api.images(filters), placeholderData: keepPreviousData, refetchInterval: busy ? 8000 : false });
  // `open` comes from the URL until the user navigates within the modal; `undefined` = follow the URL.
  const [openState, setOpenState] = useState<number | null | undefined>(undefined);
  const open = openState === undefined ? (sp.get('open') ? Number(sp.get('open')) : null) : openState;
  const setOpen = (v: number | null) => setOpenState(v);

  const set = (k: string, v: string | undefined) => setMany({ [k]: v });
  const setMany = (kv: Record<string, string | undefined>) => {
    const n = new URLSearchParams(sp);
    for (const [k, v] of Object.entries(kv)) if (v === undefined || v === '') n.delete(k); else n.set(k, v);
    if (!('offset' in kv)) n.delete('offset');
    n.delete('open');
    setOpenState(undefined);
    setSp(n);
  };
  const items = useMemo(() => data?.items ?? [], [data]);
  // Stable between renders, so the detail view's hotkey listener isn't re-bound on every poll.
  const nav = useCallback((dir: 1 | -1) => {
    if (open === null) return;
    const i = items.findIndex((x) => x.id === open);
    const nx = items[i + dir];
    if (nx?.id) setOpenState(nx.id);
  }, [items, open]);
  const total = data?.total ?? 0;
  const offset = filters.offset ?? 0;
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1.5 sm:py-1 text-sm';
  const [showFilters, setShowFilters] = useState(false);
  const nActive = ['folder', 'tier', 'keeper', 'subject', 'status', 'review', 'truth_mismatch', 'lr_rating', 'recursive', 'reviewed', 'rating'].filter((k) => sp.get(k)).length;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 sm:hidden">
        <input value={filters.q ?? ''} onChange={(e) => set('q', e.target.value)} placeholder="search keywords / name" className={`${sel} flex-1 min-w-0`} />
        <button onClick={() => setShowFilters(!showFilters)} aria-expanded={showFilters}
          className={`shrink-0 px-3 py-1.5 rounded border text-sm active:bg-gray-800 ${showFilters || nActive ? 'border-blue-500 text-blue-200' : 'border-gray-700 text-gray-300'}`}>
          filters{nActive ? ` · ${nActive}` : ''}
        </button>
      </div>
      <div className={`${showFilters ? 'grid' : 'hidden'} grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center animate-[menu-in_120ms_ease-out] sm:animate-none`}>
        <input value={filters.folder} onChange={(e) => set('folder', e.target.value)} placeholder="folder (relative to photos root)" className={`${sel} col-span-2 sm:w-72`} />
        <label className="order-last sm:order-none text-sm sm:text-xs text-gray-400 flex items-center gap-1.5 sm:gap-1 py-1 sm:py-0"><input type="checkbox" checked={filters.recursive} onChange={(e) => set('recursive', e.target.checked ? undefined : 'false')} /> <Tip tip="Include photos in subfolders of the folder above. Off shows only that folder's own files.">recursive</Tip></label>
        <Tip plain tip="Filters on the tier each tile shows: your call if you set one, otherwise the tier from the focus_source setting (vision model by default, local until the model has run)."><select value={filters.tier ?? ''} onChange={(e) => set('tier', e.target.value)} className={`${sel} w-full sm:w-auto`}>
          <option value="">any focus</option><option value="2">2 · sharp</option><option value="1">1 · partial</option><option value="0">0 · nobody</option>
        </select></Tip>
        <select value={filters.keeper === undefined ? '' : String(filters.keeper)} onChange={(e) => set('keeper', e.target.value)} className={`${sel} w-full sm:w-auto`}>
          <option value="">keeper?</option><option value="true">keepers</option><option value="false">culls</option>
        </select>
        <Tip plain tip="Photos you have or haven't rated yet (q/w/e/r in the photo view), or just your bangers. Pick “not reviewed yet” to cull: rating a photo steps to the next one."><select value={filters.rating === 3 ? 'banger' : filters.reviewed === undefined ? '' : String(filters.reviewed)}
          onChange={(e) => setMany(e.target.value === 'banger' ? { rating: '3', reviewed: undefined } : { rating: undefined, reviewed: e.target.value })} className={`${sel} w-full sm:w-auto`}>
          <option value="">reviewed?</option><option value="false">not reviewed yet</option><option value="true">reviewed</option><option value="banger">★ bangers</option>
        </select></Tip>
        <select value={filters.subject ?? ''} onChange={(e) => set('subject', e.target.value)} className={`${sel} w-full sm:w-auto`}>
          <option value="">any subject</option>{SUBJECTS.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
        <Tip plain tip="pending: registered, not analyzed yet · analyzed: local focus scoring done · tagged: the vision model has run too · error: a stage failed (details in the photo)."><select value={filters.status ?? ''} onChange={(e) => set('status', e.target.value)} className={`${sel} w-full sm:w-auto`}>
          <option value="">any status</option><option value="pending">pending</option><option value="analyzed">analyzed</option><option value="tagged">tagged</option><option value="error">error</option>
        </select></Tip>
        <label className="order-last sm:order-none text-sm sm:text-xs text-gray-400 flex items-center gap-1.5 sm:gap-1 py-1 sm:py-0"><input type="checkbox" checked={!!filters.review} onChange={(e) => set('review', e.target.checked ? '1' : undefined)} /> <Tip tip="Photos where the local sharpness tier and the vision model's tier differ. These are the cases where one of them is wrong; open one to see both sides.">needs review</Tip></label>
        <label className="order-last sm:order-none text-sm sm:text-xs text-gray-400 flex items-center gap-1.5 sm:gap-1 py-1 sm:py-0"><input type="checkbox" checked={!!filters.truth_mismatch} onChange={(e) => set('truth_mismatch', e.target.checked ? '1' : undefined)} /> <Tip tip="Photos whose shown tier differs from the ground truth you imported on the Calibrate page. Use it to see what the thresholds or the model get wrong.">≠ ground truth</Tip></label>
        <select value={filters.lr_rating ?? ''} onChange={(e) => set('lr_rating', e.target.value)} className={`${sel} w-full sm:w-auto`}>
          <option value="">any LR rating</option>{[0, 1, 2, 3, 4, 5].map((r) => <option key={r} value={r}>LR {r}★</option>)}
        </select>
        <input value={filters.q ?? ''} onChange={(e) => set('q', e.target.value)} placeholder="search keywords / name" className={`${sel} hidden sm:block w-56`} />
        <Tip plain tip="Eye sharpness sorts by the eye-band Laplacian (photos without located eyes go last). Head sharpness uses the head box. Score is the model's 1–5 (or yours)."><select value={filters.sort} onChange={(e) => set('sort', e.target.value)} className={`${sel} w-full sm:w-auto`}>
          <option value="path">by path</option><option value="newest">newest</option><option value="score">by score</option><option value="eye_sharpness">by eye sharpness</option><option value="sharpness">by head sharpness</option><option value="lr">by your LR rating</option>
        </select></Tip>
        <span className="hidden sm:inline ml-auto text-xs text-gray-500">{isFetching ? 'loading…' : `${total} photos`}</span>
      </div>
      <div className="sm:hidden -mt-2 text-xs text-gray-500">{isFetching ? 'loading…' : `${total} photos`}</div>

      <div className="grid gap-2 grid-cols-2 sm:grid-cols-[repeat(auto-fill,minmax(180px,1fr))]">
        {items.map((it) => (
          <button key={it.id ?? it.rel} onClick={() => it.id && setOpen(it.id)} className="group text-left rounded-lg overflow-hidden border border-gray-800 bg-gray-900 hover:border-gray-600 transition active:scale-[0.97] active:border-gray-500">
            <div className="aspect-[3/2] bg-gray-950 relative">
              {it.id && it.status !== 'pending' ? <img src={thumbUrl(it.id)} alt="" loading="lazy" className="w-full h-full object-cover" /> : <div className="w-full h-full flex items-center justify-center text-gray-700 text-xs">pending</div>}
              <div className="absolute top-1 left-1 flex gap-1">
                <Tip plain tip={<>Focus tier shown: {it.overridden ? 'your call if you set focus, otherwise ' : ''}the configured source. Local {it.focus_tier_local ?? '–'}, model {it.focus_tier_vlm ?? '–'}. Open the photo for the reasoning.</>}><TierBadge tier={it.focus_tier} small /></Tip>
                {it.truth_tier !== null && it.truth_tier !== undefined && <Tip plain tip={<>Your ground truth: tier {it.truth_tier}. {it.truth_tier === it.focus_tier ? 'Matches the shown tier.' : `Differs from the shown tier (${it.focus_tier ?? '–'}).`}</>}><span className={`rounded px-1 text-[10px] ${it.truth_tier === it.focus_tier ? 'bg-emerald-900/80 text-emerald-200' : 'bg-red-900/80 text-red-200'}`}>T{it.truth_tier}</span></Tip>}
                {it.review && <Tip plain tip={<>Local sharpness says tier {it.focus_tier_local}, the vision model says tier {it.focus_tier_vlm}. Open it to see why each decided that, then set your call.</>}><span className="rounded bg-amber-900/80 text-amber-200 px-1 text-[10px]">review</span></Tip>}
                {it.reviewed
                  ? <RatingBadge rating={it.rating} />
                  : it.overridden && <Tip plain tip="You set a score, keep/cull or note on this photo. Your values beat the automatic ones."><span className="rounded bg-purple-900/80 text-purple-200 px-1 text-[10px]">edited</span></Tip>}
              </div>
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
          <button disabled={offset === 0} onClick={() => set('offset', String(Math.max(0, offset - PAGE)))} className="px-4 py-2 sm:px-3 sm:py-1 rounded bg-gray-800 active:bg-gray-700 disabled:opacity-40">← prev</button>
          <span className="text-gray-500">{offset + 1}–{Math.min(offset + PAGE, total)} of {total}</span>
          <button disabled={offset + PAGE >= total} onClick={() => set('offset', String(offset + PAGE))} className="px-4 py-2 sm:px-3 sm:py-1 rounded bg-gray-800 active:bg-gray-700 disabled:opacity-40">next →</button>
        </div>
      )}
      {open !== null && <ImageDetail id={open} onClose={() => setOpen(null)} onNav={nav} />}
    </div>
  );
}
