import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, cropUrl, RATINGS } from '../api/client';
import { TierBadge, Stars } from './TierBadge';
import Tip from './Tip';
import FrameOverlay from './FrameOverlay';
import FrameViewer from './FrameViewer';
import LayerBar from './LayerBar';
import RatingsStrip from './RatingsStrip';
import { FocusMath, PersonInspector } from './PersonInspector';
import { gradePerson } from '../lib/pose';
import useHotkeys from '../hooks/useHotkeys';
import useStore from '../hooks/useStore';
import { METRIC_TIPS, TIER_MEANING, explainDisagree, explainLocal, explainPrior, explainSplit, splitShort } from '../lib/explain';

function Row({ k, v, tip }: { k: string; v: React.ReactNode; tip?: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[88px_1fr] sm:grid-cols-[110px_1fr] gap-2 text-sm py-0.5">
      <span className="text-gray-500">{tip ? <Tip tip={tip}>{k}</Tip> : k}</span>
      <span className="text-gray-200 break-words">{v}</span>
    </div>
  );
}

// Inline (Review tab) on xl+: people and focus math | frame and crop | model and your call. The second row
// is 1fr so the tall frame and sidebar spans don't open a gap between people and focus math.
const WIDE = {
  grid: 'xl:grid-cols-[340px_minmax(0,1fr)_360px] 2xl:grid-cols-[420px_minmax(0,1fr)_400px] xl:grid-rows-[auto_1fr] xl:[--frame-h:78vh]',
  frame: 'xl:col-start-2 xl:row-span-2',
  people: 'xl:col-span-1 xl:col-start-1 xl:row-start-1',
  math: 'xl:col-span-1 xl:col-start-1 xl:row-start-2 xl:self-start',
  side: 'xl:col-start-3 xl:row-span-2',
};

/** A modal over the gallery; without `onClose` it renders inline as the page itself (the Review tab). */
export default function ImageDetail({ id, onClose, onNav, toolbar }: {
  id: number; onClose?: () => void; onNav?: (dir: 1 | -1) => void; toolbar?: React.ReactNode;   // toolbar: extra controls left of the arrows
}) {
  const inline = !onClose;
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ['image', id], queryFn: () => api.image(id) });
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config, staleTime: 30_000 });
  const [note, setNote] = useState('');
  const layerList = useStore((s) => s.layers);
  const layers = useMemo(() => new Set(layerList), [layerList]);
  const toggle = useStore((s) => s.toggleLayer);
  const [full, setFull] = useState(false);
  const zoomRef = useRef(0);   // the fullscreen viewer's magnification, kept while stepping between photos
  const openFull = useCallback((s: number) => { zoomRef.current = s; setFull(true); }, []);
  useEffect(() => {  // the gallery behind must not scroll while this is open (wheel over the backdrop, or past the end)
    if (inline) return;
    const html = document.documentElement;
    const prev = { overflow: html.style.overflow, gutter: html.style.scrollbarGutter };
    html.style.scrollbarGutter = 'stable';   // keep the scrollbar's space so the gallery doesn't shift sideways
    html.style.overflow = 'hidden';
    return () => { html.style.overflow = prev.overflow; html.style.scrollbarGutter = prev.gutter; };
  }, [inline]);
  const closeFull = useCallback(() => setFull(false), []);
  const showMath = useStore((s) => s.showMath);
  const setShowMath = useStore((s) => s.setShowMath);
  const [sel, setSel] = useState({ id, person: 0 });   // the inspected person resets when navigating to another image
  const person = sel.id === id ? sel.person : 0;
  const setPerson = useCallback((i: number) => setSel({ id, person: i }), [id]);
  const needDebug = (layers.has('heatmap') || showMath) && !!data?.local;
  const dbg = useQuery({ queryKey: ['focus-debug', id], queryFn: () => api.focusDebug(id), enabled: needDebug, staleTime: 5 * 60_000, retry: false });
  // The id travels with the mutation: rating advances to the next photo before the save lands.
  const ovm = useMutation({
    mutationFn: ({ id, o }: { id: number; o: Parameters<typeof api.override>[1] }) => api.override(id, o),
    onSuccess: (_d, { id }) => {
      qc.invalidateQueries({ queryKey: ['image', id] });
      qc.invalidateQueries({ queryKey: ['images'] });
      qc.invalidateQueries({ queryKey: ['tree'] });
    },
  });
  const save = ovm.mutate;   // stable across renders
  const ov = { mutate: (o: Parameters<typeof api.override>[1]) => save({ id, o }) };
  // Culling: q/w/e/r/t (or the bottom bar on a phone) rate the photo, mark it reviewed, and move on to the next one.
  const rate = useCallback((r: number) => { save({ id, o: { rating: r } }); onNav?.(1); }, [save, id, onNav]);
  // Backspace takes your rating back off (the photo is unreviewed again) and stays put.
  const unrate = useCallback(() => save({ id, o: { clear_rating: true } }), [save, id]);
  useHotkeys({
    ...(onClose && { Escape: onClose }),
    ...(onNav && { ArrowRight: () => onNav(1), ArrowLeft: () => onNav(-1) }),
    ...Object.fromEntries(RATINGS.map((r) => [r.key, (e: KeyboardEvent) => { if (!e.repeat) rate(r.value); }])),
    Backspace: (e) => { if (!e.repeat && data?.reviewed) unrate(); },
  });
  const v = data?.vlm;
  const l = data?.local;
  // Once per result, not on every keystroke in the note field or every poll.
  const grades = useMemo(() => (l?.people ?? []).map((q) => gradePerson(q, cfg)), [l, cfg]);
  const localTip = useMemo(() => l && explainLocal(l, cfg), [l, cfg]);
  const p = l?.people?.[0];
  const ratings = data && <RatingsStrip d={data} cfg={cfg} localTip={localTip} />;
  const heat = dbg.data?.heatmap;
  // Horizontal swipe on a touch screen steps to the next/previous photo (vertical scrolling wins when ambiguous).
  const swipe = useRef<{ x: number; y: number } | null>(null);
  const onTouchStart = (e: React.TouchEvent) => {
    const t = e.touches[0];
    const scroller = (e.target as HTMLElement).closest('input, textarea, .overflow-x-auto');
    swipe.current = e.touches.length === 1 && !scroller && !full ? { x: t.clientX, y: t.clientY } : null;
  };
  const onTouchEnd = (e: React.TouchEvent) => {
    const s0 = swipe.current, t = e.changedTouches[0];
    swipe.current = null;
    if (!s0 || !onNav) return;
    const dx = t.clientX - s0.x, dy = t.clientY - s0.y;
    if (Math.abs(dx) > 70 && Math.abs(dx) > 2 * Math.abs(dy)) onNav(dx < 0 ? 1 : -1);
  };
  const layerBar = <LayerBar layers={layers} toggle={toggle} heat={heat} heatLoading={dbg.isFetching} />;
  return (
    <div className={inline ? '' : 'fixed inset-0 z-50 flex'}>
      {!inline && <div className="absolute inset-0 bg-black/70 animate-[fade-in_150ms_ease-out]" onClick={onClose} />}
      <div onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}
        className={inline ? 'relative bg-gray-950'
          : 'relative sm:m-auto w-full h-[100dvh] sm:h-auto sm:w-[min(1200px,96vw)] sm:max-h-[94vh] overflow-auto overscroll-contain sm:rounded-xl sm:border border-gray-700 bg-gray-950 shadow-2xl animate-[sheet-in_180ms_ease-out]'}>
        <div className={`flex flex-wrap items-center gap-x-3 gap-y-1 px-3 sm:px-4 py-2 border-b border-gray-800 sticky z-10 bg-gray-950/95 backdrop-blur ${inline ? 'top-[calc(3.5rem+env(safe-area-inset-top))]' : 'top-0 pt-[max(0.5rem,env(safe-area-inset-top))] sm:pt-2'}`}>
          <span className="font-mono text-sm text-gray-300 truncate min-w-0 flex-1 sm:flex-none">{data?.rel ?? id}</span>
          <span className="sm:ml-auto flex items-center gap-1 sm:gap-2">
            {toolbar}
            {onNav && <button onClick={() => onNav(-1)} aria-label="Previous photo" className="px-3 py-1.5 sm:px-2 sm:py-0 rounded text-gray-400 hover:text-white active:bg-gray-800">←</button>}
            {onNav && <button onClick={() => onNav(1)} aria-label="Next photo" className="px-3 py-1.5 sm:px-2 sm:py-0 rounded text-gray-400 hover:text-white active:bg-gray-800">→</button>}
            {onClose && <button onClick={onClose} aria-label="Close" className="px-3 py-1.5 sm:px-2 sm:py-0 rounded text-gray-400 hover:text-white active:bg-gray-800">✕</button>}
          </span>
          {/* Every verdict side by side under the name, the same row as the viewer's. */}
          {(ratings || ovm.isError) && (
            <div className="basis-full flex flex-wrap items-center gap-x-4 gap-y-1">
              {ratings}
              {data?.overridden && <Tip plain tip="You set at least one value under “Your call”. Your values beat the local and model results everywhere, including exports."><span className="text-[11px] text-purple-300">overridden</span></Tip>}
              {ovm.isError && <span className="text-xs text-red-400">couldn't save: {String(ovm.error?.message ?? ovm.error)}</span>}
            </div>
          )}
        </div>
        <div className={`grid md:grid-cols-[minmax(0,1fr)_380px] gap-4 p-3 sm:p-4 pb-[max(1rem,env(safe-area-inset-bottom))] ${inline ? WIDE.grid : ''}`}>
          {/* Frame and crop sit beside the sidebar; the people and focus-math panels run the full width below
              both on md+ (grid placement, so the DOM order, and the stacking on phones, stays the same).
              Inline on a wide screen, people and focus math move to a left column and the frame grows. */}
          <div className={`space-y-3 min-w-0 md:col-start-1 md:row-start-1 ${inline ? WIDE.frame : ''}`}>
            {l && layerBar}
            <FrameOverlay id={id} l={l} grades={grades} layers={layers} selected={person} onSelect={setPerson} heat={heat} onOpen={() => openFull(0)} />
            {full && l && <FrameViewer id={id} name={data?.rel ?? String(id)} l={l} grades={grades} layers={layers} selected={person} onSelect={setPerson} heat={heat} bar={layerBar}
              ratings={ratings} zoomRef={zoomRef} onClose={closeFull} onNav={onNav} />}
            {data?.has_crop && (
              <div className="flex flex-col sm:flex-row gap-3 items-start">
                <img src={cropUrl(id)} alt="head crop" onClick={l ? () => openFull(1) : undefined} title={l ? 'Open at 1:1 on the head' : undefined}
                  className={`w-full max-w-64 sm:w-64 shrink-0 rounded-lg bg-gray-900 ${l ? 'cursor-zoom-in' : ''}`} />
                <div className="text-xs text-gray-400 space-y-1 min-w-0 [overflow-wrap:anywhere]">
                  <div>Native-resolution crop of the primary subject's head and upper body (what the model judges focus from). Hover any number for what it means.</div>
                  {p && l && (
                    <div className="font-mono text-gray-300 space-y-0.5">
                      {p.sharp_eye != null
                        ? <div><Tip tip={METRIC_TIPS.eyes}>eyes {p.sharp_eye}</Tip> · <Tip tip={METRIC_TIPS.fft}>fft {p.hf_eye ?? '–'}</Tip> <span className="text-gray-500">(via <Tip tip={METRIC_TIPS.eyeSrc[p.eye_src ?? 'pose']}>{p.eye_src === 'face' ? 'face landmarks' : 'pose keypoints'}</Tip>)</span></div>
                        : <div className="text-gray-500"><Tip tip={METRIC_TIPS.noEyes}>eyes not located</Tip></div>}
                      <div>
                        <Tip tip={METRIC_TIPS.head}>head {p.sharp_head ?? '–'}</Tip> · <Tip tip={METRIC_TIPS.torso}>torso {p.sharp_torso ?? '–'}</Tip> · <Tip tip={METRIC_TIPS.body}>body {p.sharp_body ?? '–'}</Tip> · <Tip tip={METRIC_TIPS.bg}>bg {l.bg_sharp ?? '–'}</Tip>
                      </div>
                      <div className="text-gray-500">
                        <Tip tip={METRIC_TIPS.headSrc[p.head_src]}>head via {p.head_src}</Tip> · <Tip tip={METRIC_TIPS.people}>{l.n_people} people</Tip> · <Tip tip={localTip}>local tier {l.local_tier} ({l.local_reason})</Tip>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
          {l && <div className={`min-w-0 md:col-span-2 ${inline ? WIDE.people : ''}`}><PersonInspector l={l} cfg={cfg} grades={grades} localTip={localTip} selected={person} onSelect={setPerson} /></div>}
          {l && l.people?.length > 0 && (
            <div className={`rounded-lg border border-gray-800 p-3 space-y-2 min-w-0 md:col-span-2 ${inline ? WIDE.math : ''}`}>
              <button onClick={() => setShowMath(!showMath)} className="text-xs uppercase tracking-wide text-gray-500 hover:text-gray-300">
                {showMath ? '▾' : '▸'} focus math · person #{person + 1}
              </button>
              {showMath && <FocusMath d={dbg.data?.people?.[person]} p={l.people[person]} loading={dbg.isFetching && !dbg.data} error={dbg.error ? String(dbg.error.message) : undefined} />}
            </div>
          )}
          <div className={`space-y-4 min-w-0 md:col-start-2 md:row-start-1 ${inline ? WIDE.side : ''}`}>
            {v ? (
              <div>
                <Row k="model tier" v={<><TierBadge tier={v.focus_tier} />{data?.vlm_stale && <span className="ml-2 text-xs text-amber-300">stale · re-tag pending</span>}</>} tip={<>The vision model's own focus verdict, from the downscaled frame plus the native-resolution crop, with the local numbers passed as evidence. Tier {v.focus_tier} means {TIER_MEANING[v.focus_tier]}.</>} />
                <Row k="focus notes" v={v.focus_notes} tip="The model's reason for its tier: where focus landed, and whether blur looks like missed focus (the whole subject soft while something else is crisp) or motion (a directional smear)." />
                <Row k="subject" v={`${v.primary_subject} · ${v.composition} · ${v.subject_placement}`} tip="Subject category · how much of the primary person is in frame · where they sit in the frame. From the model. The 4B model is shaky on subject labels for candid shots, so treat them as hints." />
                <Row k="action" v={v.action} />
                <Row k="people" v={v.people_count} />
                <Row k="description" v={v.description} />
                <Row k="keywords" v={<span className="flex flex-wrap gap-1">{v.keywords.map((k) => <span key={k} className="rounded bg-gray-800 px-1.5 text-xs">{k}</span>)}</span>} />
                <Row k="adjectives" v={<span className="flex flex-wrap gap-1">{v.adjectives.map((k) => <span key={k} className="rounded bg-gray-800/60 px-1.5 text-xs text-gray-300">{k}</span>)}</span>} />
                <Row k="remarks" v={v.quality_remarks} tip="Editor-style cull notes from the model: exposure, blur, noise, clipping, distractions, crop." />
                <Row k="score" v={<><Stars n={v.quality_score} /> {v.keeper ? 'keeper' : 'cull'}</>} tip="The model's overall quality (1–5) and whether a photographer would deliver it. Your call overrides both." />
              </div>
            ) : (
              <p className="text-sm text-gray-500">{data?.status === 'skipped' ? `Skipped by the vision model (${data.vlm_skip}): the job had "skip nobody-in-focus" on. Re-tag with it off to have the model look.` : data?.status === 'analyzed' ? 'Not yet tagged by the vision model.' : data?.error ?? 'Not processed.'}</p>
            )}
            {l && !l.af && l.af_note && <div className="text-xs text-gray-600">AF: {l.af_note}</div>}
            {l?.af && (
              <div className="text-xs text-gray-400">
                <Tip tip="Read from the camera maker notes. The person under the active AF points is who the photographer meant, so they become the primary subject even when someone else is bigger or sharper; the focus tier then says whether focus actually landed on them."><span className="text-gray-500">AF:</span></Tip>{' '}
                {l.af.mode_name}{l.af.user_placed ? '' : ' (camera-chosen)'} · {l.af.active.length} active of {l.af.n_points} points ({l.af.active_from === 'in_focus' ? 'reported focus' : 'selected'})
                {' · '}{l.primary_by === 'af' ? <span className="text-red-300">primary subject picked by AF</span> : l.af.active.length ? 'not on any detected person, primary by prominence' : 'no active points'}
              </div>
            )}
            {l?.exif_prior?.summary && (
              <div className="text-xs text-gray-400">
                <Tip tip="Read from the file's EXIF. The camera summary goes into the model's context. Motion risk can also demote a borderline local tier 3 to 2."><span className="text-gray-500">camera:</span></Tip> {l.exif_prior.summary}
                {l.exif_prior.motion_risk === 'high' && <Tip plain tip={explainPrior(l.exif_prior, cfg).motion}><span className="ml-1 rounded bg-amber-900/60 px-1 text-amber-200 cursor-help">motion risk</span></Tip>}
                {l.exif_prior.dof_risk === 'high' && <Tip plain tip={explainPrior(l.exif_prior, cfg).dof}><span className="ml-1 rounded bg-sky-900/60 px-1 text-sky-200 cursor-help">shallow DOF</span></Tip>}
              </div>
            )}
            {data?.local && (
              <div className="text-xs text-gray-500">
                <Tip tip={localTip}>local: tier {data.local.local_tier}</Tip> · {data.local.width}×{data.local.height} {data.local.orientation}
                {data.local.exposure && <> · <Tip tip={`Underexposed (scene key ${data.local.exposure.key}): brightened by ${data.local.exposure.ev} stops before detection, scoring, and the model's frame and crop. The viewer shows the brightened image.`} className="text-amber-300">+{data.local.exposure.ev} EV</Tip></>}
                {data.vlm_stale ? (
                  <> · <Tip tip="The model's verdict predates this exposure lift: it judged the frame as it was before (usually the dark original). The next job over this folder re-tags it; until then the model's tier, notes and remarks describe the old frame." className="text-amber-300">model saw the unlifted frame</Tip></>
                ) : data.focus_tier_local !== null && data.focus_tier_vlm != null && data.focus_tier_local !== data.focus_tier_vlm && (
                  <> · <Tip tip={explainDisagree(data.local, data.focus_tier_vlm, v?.focus_notes, cfg)} className="text-amber-300">local ({data.focus_tier_local}) and model ({data.focus_tier_vlm}) disagree</Tip></>
                )}
                {data.local.split && <> · <Tip tip={explainSplit(data.local.split, cfg)} className="text-amber-300">metrics split ({splitShort(data.local.split)})</Tip></>}
              </div>
            )}
            <div className="rounded-lg border border-gray-800 p-3 space-y-2">
              <div className="text-xs uppercase tracking-wide text-gray-500"><Tip tip="Your overrides. They beat the local and model results in the grid, the filters, and every export (tree, CSV, XMP). Reset clears them. They aren't used as calibration truth; import your exported ratings for that.">Your call</Tip></div>
              <div className="flex flex-wrap gap-1 text-xs items-center">
                <span className="text-gray-500 w-14"><Tip tip="Your cull: 0 missed, 1 soft, 2 slightly soft, 3 sharp set the focus tier; ★ marks a banger (sharp, and one of the best). Keys q w e r t. Rating marks the photo reviewed and moves to the next one; clear (⌫) takes it back off.">rating</Tip></span>
                {RATINGS.map((r) => (
                  <button key={r.value} onClick={() => rate(r.value)} title={`${r.label} (${r.key})`}
                    className={`px-3 py-2 sm:px-2 sm:py-1 rounded border transition active:scale-95 ${data?.rating === r.value ? r.cls : 'border-gray-700 hover:border-gray-500'}`}>
                    {r.short} <kbd className="hidden sm:inline text-[10px] text-gray-500">{r.key}</kbd>
                  </button>
                ))}
                {data?.reviewed && <button onClick={unrate} title="Clear your rating (Backspace)" className="ml-auto px-2 py-2 sm:py-1 text-gray-400 hover:text-white">clear <kbd className="hidden sm:inline text-[10px] text-gray-500">⌫</kbd></button>}
              </div>
              <div className="flex flex-wrap gap-1 text-xs items-center">
                <span className="text-gray-500 w-14">score</span>
                {[1, 2, 3, 4, 5].map((s) => (
                  <button key={s} onClick={() => ov.mutate({ quality_score: s })} className={`px-3 py-2 sm:px-2 sm:py-1 rounded border transition active:scale-95 ${data?.quality_score === s ? 'border-amber-500 bg-amber-900/30' : 'border-gray-700 hover:border-gray-500'}`}>{s}</button>
                ))}
              </div>
              <div className="flex flex-wrap gap-1 text-xs items-center">
                <span className="text-gray-500 w-14">keep</span>
                <button onClick={() => ov.mutate({ keeper: true })} className={`px-3 py-2 sm:px-2 sm:py-1 rounded border transition active:scale-95 ${data?.keeper === true ? 'border-emerald-500 bg-emerald-900/30' : 'border-gray-700'}`}>keeper</button>
                <button onClick={() => ov.mutate({ keeper: false })} className={`px-3 py-2 sm:px-2 sm:py-1 rounded border transition active:scale-95 ${data?.keeper === false ? 'border-red-500 bg-red-900/30' : 'border-gray-700'}`}>cull</button>
                {data?.overridden && <button onClick={() => ov.mutate({ clear: true })} className="ml-auto text-gray-400 hover:text-white">reset</button>}
              </div>
              <div className="flex gap-1 text-xs">
                <input value={note} onChange={(e) => setNote(e.target.value)} placeholder={data?.override?.note ?? 'note'} className="flex-1 min-w-0 bg-gray-900 border border-gray-700 rounded px-2 py-1.5 sm:py-1" />
                <button onClick={() => { ov.mutate({ note }); setNote(''); }} className="px-3 sm:px-2 rounded border border-gray-700 active:bg-gray-800">save</button>
              </div>
            </div>
          </div>
        </div>
        <div className="sm:hidden sticky bottom-0 z-10 grid grid-cols-5 gap-2 px-3 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] border-t border-gray-800 bg-gray-950/95 backdrop-blur">
          {RATINGS.map((r) => (
            <button key={r.value} onClick={() => rate(r.value)} aria-label={`Rate ${r.label}`}
              className={`h-12 rounded-lg border text-lg font-semibold transition active:scale-95 ${data?.rating === r.value ? `${r.solid} ring-2 ring-white/70` : r.cls}`}>
              {r.short}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
