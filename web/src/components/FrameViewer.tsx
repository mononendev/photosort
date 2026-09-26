import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { frameUrl, fullUrl } from '../api/client';
import type { FocusDebug, LocalResult } from '../api/client';
import type { Cfg } from '../lib/explain';
import type { Layer } from '../lib/pose';
import { HoverBar, OverlaySvg } from './FrameOverlay';
import type { Hover } from './FrameOverlay';

const MAX_ZOOM = 40;
const FRAME_LONG_EDGE = 1568;   // the cached frame; past this on screen we swap in the full-resolution render

// The stage fills the screen; the toolbar and the hover readout float over it (fixed height), so nothing they show can
// resize the stage and shift the zoom. Fit-to-screen leaves room for them (the toolbar is measured: it wraps on phones).
const INSET_T = 52, INSET_B = 72;
const fitOf = (w: number, h: number, W: number, H: number, top = INSET_T) => {
  const f = Math.max(0.01, Math.min(w / W, (h - top - INSET_B) / H));
  return { f, ox: (w - W * f) / 2, oy: top + (h - top - INSET_B - H * f) / 2 };
};

type View = { zoom: number; ox: number; oy: number };   // zoom over fit-to-screen; image top-left in the stage, px

/**
 * Fullscreen frame with the same overlay. Scroll zooms around the cursor, drag pans, double-click (or 0) resets,
 * + / − zoom around the center. Escape closes the viewer only; arrow keys still step through images.
 * On touch screens: pinch zooms around the fingers, one finger pans, double-tap zooms in (or back to fit).
 */
export default function FrameViewer({ id, name, l, cfg, layers, selected, onSelect, heat, bar, onClose, onNav }: {
  id: number; name: string; l: LocalResult; cfg: Cfg; layers: Set<Layer>; selected: number; onSelect: (i: number) => void;
  heat?: FocusDebug['heatmap']; bar: ReactNode; onClose: () => void; onNav?: (dir: 1 | -1) => void;
}) {
  const W = l.width, H = l.height;
  const stage = useRef<HTMLDivElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const toolbar = useRef<HTMLDivElement>(null);
  const insetT = () => toolbar.current?.offsetHeight ?? INSET_T;
  const [box, setBox] = useState({ w: 0, h: 0, t: INSET_T });
  const [view, setView] = useState<View>({ zoom: 1, ox: 0, oy: 0 });
  const [hover, setHover] = useState<Hover>(null);
  const [loadedFor, setLoadedFor] = useState<number | null>(null);
  const drag = useRef<{ x: number; y: number; ox: number; oy: number; moved: boolean } | null>(null);
  const dragged = useRef(false);
  const selectUnlessDragged = useCallback((i: number) => { if (!dragged.current) onSelect(i); }, [onSelect]);
  const pts = useRef(new Map<number, { x: number; y: number }>());   // active pointers, client coords
  const pinch = useRef<{ d: number; mx: number; my: number; v: View } | null>(null);
  const lastTap = useRef<{ t: number; x: number; y: number } | null>(null);
  const lastType = useRef('mouse');

  const fit = box.w && box.h ? fitOf(box.w, box.h, W, H, box.t).f : 0;
  const reset = useCallback(() => {
    const el = stage.current;
    if (!el) return;
    const w = el.clientWidth, h = el.clientHeight, t = insetT(), { ox, oy } = fitOf(w, h, W, H, t);
    setBox({ w, h, t });
    setView({ zoom: 1, ox, oy });
  }, [W, H]);

  useLayoutEffect(() => {
    reset();
    root.current?.focus();
    // A window resize keeps the zoom and the point at the center of the stage.
    let last = { w: stage.current?.clientWidth ?? 0, h: stage.current?.clientHeight ?? 0, t: insetT() };
    const ro = new ResizeObserver(() => {
      const el = stage.current;
      if (!el) return;
      const w = el.clientWidth, h = el.clientHeight, t = insetT();
      if (w === last.w && h === last.h && t === last.t) return;
      const prev = last, f0 = fitOf(prev.w, prev.h, W, H, prev.t).f, fresh = fitOf(w, h, W, H, t), f1 = fresh.f;
      last = { w, h, t };
      setBox({ w, h, t });
      setView((v) => {
        if (v.zoom === 1 || !prev.w) return { zoom: 1, ox: fresh.ox, oy: fresh.oy };
        const k = f1 / f0;   // fit changed by k; scale the image about the old center, then move to the new one
        return { zoom: v.zoom, ox: w / 2 - (prev.w / 2 - v.ox) * k, oy: h / 2 - (prev.h / 2 - v.oy) * k };
      });
    });
    if (stage.current) ro.observe(stage.current);
    if (toolbar.current) ro.observe(toolbar.current);
    return () => ro.disconnect();
  }, [reset, W, H]);

  const zoomAt = useCallback((factor: number, mx: number, my: number) => {
    setView((v) => {
      const el = stage.current;
      if (!el) return v;
      const zoom = Math.min(MAX_ZOOM, Math.max(1, v.zoom * factor));
      const k = zoom / v.zoom;
      if (zoom === 1) { const { ox, oy } = fitOf(el.clientWidth, el.clientHeight, W, H, toolbar.current?.offsetHeight); return { zoom, ox, oy }; }
      return { zoom, ox: mx - (mx - v.ox) * k, oy: my - (my - v.oy) * k };
    });
  }, [W, H]);

  useEffect(() => {  // on window, capture phase: works wherever focus is, and the detail view never sees these keys
    const onKey = (e: KeyboardEvent) => {
      const el = stage.current;
      const cx = (el?.clientWidth ?? 0) / 2, cy = (el?.clientHeight ?? 0) / 2;
      const act: Record<string, () => void> = {
        Escape: onClose, '0': reset, '+': () => zoomAt(1.5, cx, cy), '=': () => zoomAt(1.5, cx, cy), '-': () => zoomAt(1 / 1.5, cx, cy),
        ArrowRight: () => onNav?.(1), ArrowLeft: () => onNav?.(-1),
      };
      if (!act[e.key] || (e.target as HTMLElement)?.tagName === 'INPUT') return;
      e.preventDefault(); e.stopPropagation();
      act[e.key]();
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [onClose, onNav, reset, zoomAt]);

  useEffect(() => {  // wheel must be non-passive to stop the page scrolling
    const el = stage.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015)), e.clientX - r.left, e.clientY - r.top);
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [zoomAt]);

  const stageRect = () => stage.current?.getBoundingClientRect() ?? { left: 0, top: 0 };
  const scale = fit * view.zoom;                        // screen px per original px
  const wantFull = scale * Math.max(W, H) > FRAME_LONG_EDGE * 1.25;
  const [fullFor, setFullFor] = useState<number | null>(null);
  if (wantFull && fullFor !== id) setFullFor(id);       // once requested, keep it for this image
  const showFull = fullFor === id;
  const fullLoaded = loadedFor === id;

  return createPortal(
    <div ref={root} tabIndex={-1} className="fixed inset-0 z-[60] bg-black/95 outline-none">
      <div ref={stage} className="absolute inset-0 overflow-hidden select-none touch-none cursor-grab active:cursor-grabbing"
        onDoubleClick={() => { if (lastType.current === 'mouse') reset(); }}
        onPointerDown={(e) => {
          lastType.current = e.pointerType;
          if (e.pointerType === 'mouse' && e.button !== 0) return;
          pts.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
          if (pts.current.size === 2) {
            const [a, b] = [...pts.current.values()], r = stageRect();
            pinch.current = { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, mx: (a.x + b.x) / 2 - r.left, my: (a.y + b.y) / 2 - r.top, v: view };
            drag.current = null;
            dragged.current = true;
            return;
          }
          if (pts.current.size > 2) return;
          drag.current = { x: e.clientX, y: e.clientY, ox: view.ox, oy: view.oy, moved: false };
          dragged.current = false;
        }}
        onPointerMove={(e) => {
          if (pts.current.has(e.pointerId)) pts.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
          const pc = pinch.current;
          if (pc && pts.current.size >= 2) {
            const [a, b] = [...pts.current.values()], r = stageRect();
            const d = Math.hypot(a.x - b.x, a.y - b.y), mx = (a.x + b.x) / 2 - r.left, my = (a.y + b.y) / 2 - r.top;
            const zoom = Math.min(MAX_ZOOM, Math.max(1, pc.v.zoom * (d / pc.d))), k = zoom / pc.v.zoom;
            setView({ zoom, ox: mx - (pc.mx - pc.v.ox) * k, oy: my - (pc.my - pc.v.oy) * k });
            return;
          }
          const d = drag.current;
          if (!d) return;
          if (e.buttons === 0) { drag.current = null; return; }   // released outside the window
          const dx = e.clientX - d.x, dy = e.clientY - d.y;
          if (!d.moved && Math.hypot(dx, dy) < 4) return;
          if (!d.moved) { d.moved = true; (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId); }
          dragged.current = true;
          setView((v) => ({ ...v, ox: d.ox + dx, oy: d.oy + dy }));
        }}
        onPointerUp={(e) => {
          pts.current.delete(e.pointerId);
          if (pinch.current) {
            if (pts.current.size >= 2) return;
            pinch.current = null;
            const rest = [...pts.current.values()][0];   // the finger still down carries on panning
            drag.current = rest ? { x: rest.x, y: rest.y, ox: view.ox, oy: view.oy, moved: true } : null;
            if (view.zoom <= 1) reset();
            return;
          }
          drag.current = null;
          if (e.pointerType !== 'touch' || dragged.current) return;
          const r = stageRect(), x = e.clientX - r.left, y = e.clientY - r.top, now = performance.now(), lt = lastTap.current;
          if (lt && now - lt.t < 320 && Math.hypot(x - lt.x, y - lt.y) < 40) {
            lastTap.current = null;
            if (view.zoom > 1) reset(); else zoomAt(3, x, y);
          } else lastTap.current = { t: now, x, y };
        }}
        onPointerCancel={(e) => { pts.current.delete(e.pointerId); pinch.current = null; drag.current = null; }}>
        {fit > 0 && (
          <div className="absolute" style={{ left: view.ox, top: view.oy, width: W * scale, height: H * scale }}>
            <img src={frameUrl(id)} alt="" draggable={false} className="absolute inset-0 w-full h-full" />
            {showFull && <img src={fullUrl(id)} alt="" draggable={false} onLoad={() => setLoadedFor(id)}
              className={`absolute inset-0 w-full h-full ${fullLoaded ? '' : 'opacity-0'}`} style={{ imageRendering: scale > 2 ? 'pixelated' : 'auto' }} />}
            <OverlaySvg l={l} cfg={cfg} layers={layers} selected={selected} heat={heat} setHover={setHover}
              zoom={0.4 + 0.6 * view.zoom} onSelect={selectUnlessDragged} />
          </div>
        )}
      </div>
      <div ref={toolbar} className="absolute inset-x-0 top-0 z-10 flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 sm:px-4 py-2 pt-[max(0.5rem,env(safe-area-inset-top))] border-b border-gray-800 bg-black/80 backdrop-blur-sm">
        <span className="font-mono text-sm text-gray-300 truncate max-w-[40vw]">{name}</span>
        {bar}
        <span className="ml-auto flex items-center gap-2 text-xs text-gray-400">
          <span className="hidden sm:inline font-mono w-32 text-right whitespace-nowrap" title="screen pixels per original pixel">{fit ? `${Math.round(scale * 100)}% of native` : ''}</span>
          <span className="hidden sm:inline text-gray-500 w-28 whitespace-nowrap">{showFull && !fullLoaded ? 'loading full res…' : showFull ? 'full resolution' : ''}</span>
          <button onClick={() => zoomAt(1 / 1.5, box.w / 2, box.h / 2)} className="px-2.5 py-1 sm:px-2 sm:py-0 rounded border border-gray-700 hover:border-gray-500 active:bg-gray-800">−</button>
          <button onClick={() => zoomAt(1.5, box.w / 2, box.h / 2)} className="px-2.5 py-1 sm:px-2 sm:py-0 rounded border border-gray-700 hover:border-gray-500 active:bg-gray-800">+</button>
          <button onClick={() => { const el = stage.current; if (el) { const k = 1 / scale; zoomAt(k, el.clientWidth / 2, el.clientHeight / 2); } }}
            className="px-2.5 py-1 sm:px-2 sm:py-0 rounded border border-gray-700 hover:border-gray-500 active:bg-gray-800" title="one screen pixel per original pixel">1:1</button>
          <button onClick={reset} className="px-2.5 py-1 sm:px-2 sm:py-0 rounded border border-gray-700 hover:border-gray-500 active:bg-gray-800">fit</button>
          <button onClick={onClose} aria-label="Close" className="px-2 text-lg text-gray-400 hover:text-white active:bg-gray-800 rounded">✕</button>
        </span>
      </div>
      <div className="absolute inset-x-0 bottom-0 z-10 flex items-start gap-4 px-4 py-1 border-t border-gray-800 bg-black/80 backdrop-blur-sm overflow-hidden"
        style={{ height: `calc(${INSET_B}px + env(safe-area-inset-bottom))`, paddingBottom: 'env(safe-area-inset-bottom)' }}>
        <div className="flex-1 min-w-0"><HoverBar hover={hover} l={l} /></div>
        <div className="hidden sm:block [@media(hover:none)]:hidden text-[11px] text-gray-600 pt-0.5">scroll to zoom · drag to pan · double-click or 0 to fit · Esc to close · ←/→ next image</div>
        <div className="hidden [@media(hover:none)]:block text-[11px] text-gray-600 pt-0.5 shrink-0">pinch to zoom · double-tap to fit</div>
      </div>
    </div>,
    document.body,
  );
}
