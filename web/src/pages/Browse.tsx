import { useRef, useState } from 'react';
import type { MouseEvent } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, thumbUrl } from '../api/client';
import type { TreeDir } from '../api/client';
import useStore from '../hooks/useStore';
import { useBusy } from '../hooks/useJobs';
import { errMsg, pct } from '../lib/format';
import { StatusDot, TierBadge, Stars } from '../components/TierBadge';
import Progress from '../components/Progress';
import Tip from '../components/Tip';

type ToggleFn = (e: MouseEvent) => void;

/** Checkbox that reports the click (with shiftKey) instead of the change, and keeps shift-click from selecting text. */
function RowCheck({ checked, onToggle }: { checked: boolean; onToggle: ToggleFn }) {
  return (
    <input type="checkbox" checked={checked} readOnly onClick={onToggle}
      onMouseDown={(e) => { if (e.shiftKey) e.preventDefault(); }} className="accent-blue-500" />
  );
}

function DirRow({ d, checked, onToggle }: { d: TreeDir; checked: boolean; onToggle: ToggleFn }) {
  const done = d.vlm_done;
  const donePct = pct(done, d.tracked);
  return (
    <div className="flex items-center gap-2 sm:gap-3 px-3 py-2.5 sm:py-2 border-b border-gray-800 hover:bg-gray-900/60">
      <RowCheck checked={checked} onToggle={onToggle} />
      <Link to={`/browse/${d.path}`} className="text-blue-300 hover:underline truncate flex-1 min-w-[7rem] py-1 -my-1">📁 {d.name}</Link>
      <span className="hidden sm:inline text-xs text-gray-500 w-28 text-right">{d.images_direct} here</span>
      <div className="w-20 sm:w-56 flex items-center gap-2">
        {d.tracked > 0 ? (
          <>
            <Progress done={done} total={d.tracked} className="flex-1" />
            <Tip tip="Tagged by the vision model / tracked (registered by a job) across this folder and its subfolders. Images never included in a job aren't tracked yet." className="hidden sm:inline text-xs text-gray-400 tabular-nums w-24 text-right">{done}/{d.tracked} · {donePct}%</Tip>
            <span className="sm:hidden text-xs text-gray-400 tabular-nums">{donePct}%</span>
          </>
        ) : (
          <span className="text-xs text-gray-600 truncate"><span className="hidden sm:inline">not processed</span><span className="sm:hidden">—</span></span>
        )}
      </div>
      {d.errors > 0 && <span className="text-xs text-red-400">{d.errors} err</span>}
      <Link to={`/photos?folder=${encodeURIComponent(d.path)}`} className="text-xs text-gray-400 hover:text-gray-200 px-1 py-1 -my-1">view</Link>
    </div>
  );
}

export default function Browse() {
  const loc = useLocation();
  const nav = useNavigate();
  const path = decodeURIComponent(loc.pathname.replace(/^\/browse\/?/, '')).replace(/\/$/, '');
  const qc = useQueryClient();
  const busy = useBusy();
  const { data, isLoading, error } = useQuery({ queryKey: ['tree', path], queryFn: () => api.tree(path), refetchInterval: busy ? 5000 : false });
  const selected = useStore((s) => s.selected);
  const toggle = useStore((s) => s.toggleSelected);
  const setSelected = useStore((s) => s.setSelected);
  const clear = useStore((s) => s.clearSelected);
  const defaults = useStore((s) => s.jobDefaults);
  const setDefaults = useStore((s) => s.setJobDefaults);
  const [msg, setMsg] = useState<string | null>(null);

  const start = useMutation({
    mutationFn: (paths: string[]) => api.createJob(paths, defaults),
    onSuccess: (job) => {
      qc.invalidateQueries({ queryKey: ['jobs'] });
      setMsg(`Queued job #${job.id} for ${job.paths.length} path(s)`);
      clear();
    },
    onError: (e) => setMsg(`Failed: ${errMsg(e)}`),
  });

  const crumbs = path ? path.split('/') : [];
  const allHere = [...(data?.dirs.map((d) => d.path) ?? []), ...(data?.files.map((f) => f.rel) ?? [])];
  const allSelected = allHere.length > 0 && allHere.every((p) => selected.includes(p));
  // Shift-click selects (or deselects) everything between the last clicked row and this one, matching
  // the state the clicked row is switching to. The anchor resets when the folder changes.
  const anchor = useRef<{ path: string; item: string } | null>(null);
  const onRowClick = (item: string) => (e: MouseEvent) => {
    const a = anchor.current;
    const from = a && a.path === path ? allHere.indexOf(a.item) : -1;
    const to = allHere.indexOf(item);
    if (e.shiftKey && from >= 0 && to >= 0) {
      const range = allHere.slice(Math.min(from, to), Math.max(from, to) + 1);
      const on = !selected.includes(item);
      setSelected(on ? [...new Set([...selected, ...range])] : selected.filter((p) => !range.includes(p)));
    } else {
      toggle(item);
    }
    anchor.current = { path, item };
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <Link to="/browse" className="text-blue-300 hover:underline">photos</Link>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-2">
            <span className="text-gray-600">/</span>
            <Link to={`/browse/${crumbs.slice(0, i + 1).join('/')}`} className="text-blue-300 hover:underline">{c}</Link>
          </span>
        ))}
        <span className="ml-auto flex items-center gap-2">
          <button onClick={() => start.mutate([path || ''])} className="text-xs px-3 py-1.5 sm:px-2 sm:py-1 rounded bg-gray-800 hover:bg-gray-700 active:bg-gray-600">
            process this folder
          </button>
        </span>
      </div>

      <div className="rounded-lg border border-gray-800 bg-gray-900/40">
        <div className="flex items-center gap-3 px-3 py-2 border-b border-gray-800 text-xs text-gray-500">
          <input type="checkbox" checked={allSelected} onChange={() => (allSelected ? setSelected(selected.filter((p) => !allHere.includes(p))) : setSelected([...new Set([...selected, ...allHere])]))} className="accent-blue-500" />
          <span className="flex-1">name <span className="hidden sm:inline text-gray-600">· shift-click to select a range</span></span>
          <span className="hidden sm:inline w-28 text-right">images</span>
          <span className="w-20 sm:w-56">tagged<span className="hidden sm:inline"> / tracked</span></span>
        </div>
        {isLoading && <div className="p-3 text-sm text-gray-500">Loading…</div>}
        {error && <div className="p-3 text-sm text-red-400">{errMsg(error)}</div>}
        {data?.dirs.map((d) => (
          <DirRow key={d.path} d={d} checked={selected.includes(d.path)} onToggle={onRowClick(d.path)} />
        ))}
        {data?.files.map((f) => (
          <div key={f.rel} className="flex items-center gap-2 sm:gap-3 px-3 py-2 sm:py-1.5 border-b border-gray-800/60 text-sm hover:bg-gray-900/60">
            <RowCheck checked={selected.includes(f.rel)} onToggle={onRowClick(f.rel)} />
            <StatusDot status={f.status} />
            <button
              onClick={() => f.id && nav(`/photos?folder=${encodeURIComponent(path)}&recursive=false&open=${f.id}`)}
              className="truncate flex-1 text-left text-gray-200 hover:text-white"
            >
              {f.name}
            </button>
            {f.id ? <img src={thumbUrl(f.id)} alt="" className="h-8 w-12 object-cover rounded bg-gray-800" loading="lazy" /> : null}
            <TierBadge tier={f.focus_tier} small />
            <span className="hidden sm:inline"><Stars n={f.quality_score} /></span>
            <span className="hidden sm:inline text-xs text-gray-500 w-24 truncate">{f.subject !== 'unknown' ? f.subject : ''}</span>
          </div>
        ))}
        {data && data.dirs.length === 0 && data.files.length === 0 && <div className="p-3 text-sm text-gray-500">Empty folder.</div>}
      </div>

      <div className="sticky bottom-[max(0.75rem,env(safe-area-inset-bottom))] sm:bottom-4 rounded-lg border border-gray-700 bg-gray-900/95 backdrop-blur p-3 flex flex-wrap items-center gap-x-4 gap-y-2 shadow-xl">
        <span className="text-sm">
          <span className="font-semibold">{selected.length}</span> selected
          {selected.length > 0 && <button onClick={clear} className="ml-2 text-xs text-gray-400 hover:text-gray-200">clear</button>}
        </span>
        <label className="text-sm flex items-center gap-1"><input type="checkbox" checked={defaults.vlm} onChange={(e) => setDefaults({ vlm: e.target.checked })} className="accent-blue-500" /> <Tip tip="After local focus scoring, send each image (frame + native-res head crop + the local numbers) to the vision model for its tier, subject, keywords and remarks. Off = local scoring only, much faster.">run vision model</Tip></label>
        <label className="text-sm flex items-center gap-1"><input type="checkbox" checked={defaults.skip_tier0} onChange={(e) => setDefaults({ skip_tier0: e.target.checked })} className="accent-blue-500" /> <Tip tip="Don't spend vision-model time on images the local stage put in tier 0 (no people, or nobody sharp). Saves time, but the model can't rescue a local false negative.">skip nobody-in-focus</Tip></label>
        <label className="text-sm flex items-center gap-1"><input type="checkbox" checked={defaults.rescan} onChange={(e) => setDefaults({ rescan: e.target.checked })} className="accent-blue-500" /> <Tip tip="Redo local scoring on images that already have it, for example after a metric change. Vision-model tags are kept unless re-tag is also on. Threshold changes alone don't need this; Calibrate's re-score is instant.">re-analyze local</Tip></label>
        <label className={`text-sm flex items-center gap-1 ${defaults.vlm ? '' : 'opacity-40'}`}><input type="checkbox" checked={!!defaults.revlm} disabled={!defaults.vlm} onChange={(e) => setDefaults({ revlm: e.target.checked })} className="accent-blue-500" /> <Tip tip="Send images that already have vision-model tags to the model again, replacing their tags, for example after a prompt or model change. Your own ratings are never touched. Needs run vision model on.">re-tag vision model</Tip></label>
        <button
          disabled={selected.length === 0 || start.isPending}
          onClick={() => start.mutate(selected)}
          className="w-full sm:w-auto sm:ml-auto px-4 py-2.5 sm:py-1.5 rounded-md bg-blue-600 hover:bg-blue-500 active:bg-blue-700 active:scale-[0.98] transition disabled:opacity-40 text-sm font-medium"
        >
          Process selected
        </button>
        {msg && <span className="text-xs text-gray-400 w-full">{msg}</span>}
      </div>
    </div>
  );
}
