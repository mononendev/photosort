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

type View = { zoom: number; ox: number; oy: number };   // zoom over fit-to-screen; image top-left in the stage, px

/**
 * Fullscreen frame with the same overlay. Scroll zooms around the cursor, drag pans, double-click (or 0) resets,
 * + / − zoom around the center. Escape closes the viewer only; arrow keys still step through images.
 */
export default function FrameViewer({ id, name, l, cfg, layers, selected, onSelect, heat, bar, onClose, onNav }: {
  id: number; name: string; l: LocalResult; cfg: Cfg; layers: Set<Layer>; selected: number; onSelect: (i: number) => void;
  heat?: FocusDebug['heatmap']; bar: ReactNode; onClose: () => void; onNav?: (dir: 1 | -1) => void;
}) {
  const W = l.width, H = l.height;
  const stage = useRef<HTMLDivElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [view, setView] = useState<View>({ zoom: 1, ox: 0, oy: 0 });
  const [hover, setHover] = useState<Hover>(null);
  const [loadedFor, setLoadedFor] = useState<number | null>(null);
  const drag = useRef<{ x: number; y: number; ox: number; oy: number; moved: boolean } | null>(null);
  const dragged = useRef(false);

  const fit = box.w && box.h ? Math.min(box.w / W, box.h / H) : 0;
  const reset = useCallback(() => {
    const el = stage.current;
    if (!el) return;
    const w = el.clientWidth, h = el.clientHeight, f = Math.min(w / W, h / H);
    setBox({ w, h });
    setView({ zoom: 1, ox: (w - W * f) / 2, oy: (h - H * f) / 2 });
  }, [W, H]);

  useLayoutEffect(() => {
    reset();
    root.current?.focus();
    // A resize (window, or the toolbar wrapping) keeps the zoom and the point at the center of the stage.
    let last = { w: stage.current?.clientWidth ?? 0, h: stage.current?.clientHeight ?? 0 };
    const ro = new ResizeObserver(() => {
      const el = stage.current;
      if (!el) return;
      const w = el.clientWidth, h = el.clientHeight;
      if (w === last.w && h === last.h) return;
      const prev = last, f0 = Math.min(prev.w / W, prev.h / H), f1 = Math.min(w / W, h / H);
      last = { w, h };
      setBox({ w, h });
      setView((v) => {
        if (v.zoom === 1 || !f0) return { zoom: 1, ox: (w - W * f1) / 2, oy: (h - H * f1) / 2 };
        const k = f1 / f0;   // fit changed by k; scale the image about the old center, then move to the new one
        return { zoom: v.zoom, ox: w / 2 - (prev.w / 2 - v.ox) * k, oy: h / 2 - (prev.h / 2 - v.oy) * k };
      });
    });
    if (stage.current) ro.observe(stage.current);
    return () => ro.disconnect();
  }, [reset, W, H]);

  const zoomAt = useCallback((factor: number, mx: number, my: number) => {
    setView((v) => {
      const el = stage.current;
      if (!el) return v;
      const f = Math.min(el.clientWidth / W, el.clientHeight / H);
      const zoom = Math.min(MAX_ZOOM, Math.max(1, v.zoom * factor));
      const k = zoom / v.zoom;
      if (zoom === 1) return { zoom, ox: (el.clientWidth - W * f) / 2, oy: (el.clientHeight - H * f) / 2 };
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

  const scale = fit * view.zoom;                        // screen px per original px
  const wantFull = scale * Math.max(W, H) > FRAME_LONG_EDGE * 1.25;
  const [fullFor, setFullFor] = useState<number | null>(null);
  if (wantFull && fullFor !== id) setFullFor(id);       // once requested, keep it for this image
  const showFull = fullFor === id;
  const fullLoaded = loadedFor === id;

  return createPortal(
    <div ref={root} tabIndex={-1} className="fixed inset-0 z-[60] flex flex-col bg-black/95 outline-none">
      <div className="flex flex-wrap items-center gap-3 px-4 py-2 border-b border-gray-800">
        <span className="font-mono text-sm text-gray-300 truncate max-w-[40vw]">{name}</span>
        {bar}
        <span className="ml-auto flex items-center gap-2 text-xs text-gray-400">
          <span className="font-mono w-32 text-right whitespace-nowrap" title="screen pixels per original pixel">{fit ? `${Math.round(scale * 100)}% of native` : ''}</span>
          <span className="text-gray-500 w-28 whitespace-nowrap">{showFull && !fullLoaded ? 'loading full res…' : showFull ? 'full resolution' : ''}</span>
          <button onClick={() => zoomAt(1 / 1.5, box.w / 2, box.h / 2)} className="px-2 rounded border border-gray-700 hover:border-gray-500">−</button>
          <button onClick={() => zoomAt(1.5, box.w / 2, box.h / 2)} className="px-2 rounded border border-gray-700 hover:border-gray-500">+</button>
          <button onClick={() => { const el = stage.current; if (el) { const k = 1 / scale; zoomAt(k, el.clientWidth / 2, el.clientHeight / 2); } }}
            className="px-2 rounded border border-gray-700 hover:border-gray-500" title="one screen pixel per original pixel">1:1</button>
          <button onClick={reset} className="px-2 rounded border border-gray-700 hover:border-gray-500">fit</button>
          <button onClick={onClose} className="px-2 text-lg text-gray-400 hover:text-white">✕</button>
        </span>
      </div>
      <div ref={stage} className="relative flex-1 overflow-hidden select-none cursor-grab active:cursor-grabbing"
        onDoubleClick={reset}
        onPointerDown={(e) => {
          if (e.button !== 0) return;
          drag.current = { x: e.clientX, y: e.clientY, ox: view.ox, oy: view.oy, moved: false };
          dragged.current = false;
        }}
        onPointerMove={(e) => {
          const d = drag.current;
          if (!d) return;
          if (e.buttons === 0) { drag.current = null; return; }   // released outside the window
          const dx = e.clientX - d.x, dy = e.clientY - d.y;
          if (!d.moved && Math.hypot(dx, dy) < 4) return;
          if (!d.moved) { d.moved = true; (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId); }
          dragged.current = true;
          setView((v) => ({ ...v, ox: d.ox + dx, oy: d.oy + dy }));
        }}
        onPointerUp={() => { drag.current = null; }}
        onPointerCancel={() => { drag.current = null; }}>
        {fit > 0 && (
          <div className="absolute" style={{ left: view.ox, top: view.oy, width: W * scale, height: H * scale }}>
            <img src={frameUrl(id)} alt="" draggable={false} className="absolute inset-0 w-full h-full" />
            {showFull && <img src={fullUrl(id)} alt="" draggable={false} onLoad={() => setLoadedFor(id)}
              className={`absolute inset-0 w-full h-full ${fullLoaded ? '' : 'opacity-0'}`} style={{ imageRendering: scale > 2 ? 'pixelated' : 'auto' }} />}
            <OverlaySvg l={l} cfg={cfg} layers={layers} selected={selected} heat={heat} setHover={setHover}
              zoom={0.4 + 0.6 * view.zoom} onSelect={(i) => { if (!dragged.current) onSelect(i); }} />
          </div>
        )}
      </div>
      <div className="px-4 py-1 border-t border-gray-800 flex items-start gap-4">
        <div className="flex-1"><HoverBar hover={hover} l={l} /></div>
        <div className="text-[11px] text-gray-600 pt-0.5">scroll to zoom · drag to pan · double-click or 0 to fit · Esc to close · ←/→ next image</div>
      </div>
    </div>,
    document.body,
  );
}
