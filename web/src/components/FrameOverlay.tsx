import { useId, useState } from 'react';
import type { ReactNode } from 'react';
import { frameUrl } from '../api/client';
import type { FocusDebug, LocalResult, Person } from '../api/client';
import { fmt } from './CheckTable';
import type { Cfg } from '../lib/explain';
import { FACE_LM, GRADE_COLOR, KP_MIN_CONF, KP_NAMES, PERSON_COLORS, SKELETON, boxH, boxW, gradePerson } from '../lib/pose';
import type { Layer } from '../lib/pose';

export type Hover = { title: string; body: ReactNode } | null;

type OverlayProps = {
  l: LocalResult; cfg: Cfg; layers: Set<Layer>; selected: number; onSelect: (i: number) => void;
  heat?: FocusDebug['heatmap']; setHover: (h: Hover) => void;
  /** Labels, dots and hit areas shrink by this so they stay the same size on screen when zoomed. */
  zoom?: number;
};

/**
 * Everything the local stage found, as an SVG in the original's pixel coordinates (any downscale of the frame
 * with the same aspect ratio fits the one viewBox). Fills its positioned parent.
 */
export function OverlaySvg({ l, cfg, layers, selected, onSelect, heat, setHover, zoom = 1 }: OverlayProps) {
  const W = l.width, H = l.height;
  const hatch = `hatch${useId().replace(/[^a-zA-Z0-9]/g, '')}`;   // unique per SVG: inline and fullscreen coexist
  const u = Math.max(W, H) / 1000 / zoom;    // one "unit" ≈ 0.1% of the long edge at zoom 1
  const fs = 16 * u;
  const on = (k: Layer) => layers.has(k);
  const hv = (title: string, body: ReactNode) => ({ onMouseEnter: () => setHover({ title, body }), onMouseLeave: () => setHover(null) });
  const stroke = (w: number) => ({ vectorEffect: 'non-scaling-stroke' as const, strokeWidth: w, fill: 'transparent' });
  const people = l.people ?? [];
  const extra = (l.mask_boxes ?? []).slice(people.length);

  const label = (x: number, y: number, text: string, color: string) => (
    <g pointerEvents="none">
      <rect x={x} y={y - fs * 1.35} width={text.length * fs * 0.6 + fs * 0.6} height={fs * 1.35} fill="rgba(0,0,0,.72)" rx={fs * 0.2} />
      <text x={x + fs * 0.3} y={y - fs * 0.35} fontSize={fs} fill={color} fontFamily="ui-monospace, monospace">{text}</text>
    </g>
  );

  const personLayer = (p: Person, i: number) => {
    const c = PERSON_COLORS[i % PERSON_COLORS.length];
    const primary = i === 0;
    const sel = i === selected;
    const g = gradePerson(p, cfg);
    const gc = GRADE_COLOR[g.grade ?? 'none'];
    const kp = p.kp;
    const [bx0, by0, bx1, by1] = p.box;
    return (
      <g key={i} opacity={selected >= 0 && !sel ? 0.55 : 1}>
        {on('people') && (
          <rect x={bx0} y={by0} width={bx1 - bx0} height={by1 - by0} stroke={c} {...stroke(primary ? 2.5 : 1.5)}
            strokeDasharray={primary ? undefined : '6 4'} className="cursor-pointer" onClick={(e) => { e.stopPropagation(); onSelect(i); }}
            {...hv(`Person #${i + 1}${primary ? ' · primary subject' : ''}`, <>
              {primary && l.primary_by === 'af' && <>picked because the camera’s AF points land on them (score {fmt(p.af_score ?? null)})<br /></>}
              detection conf {fmt(p.conf)} · {(p.area_frac * 100).toFixed(1)}% of frame · center distance {fmt(p.center_dist)}<br />
              priority {p.priority != null ? fmt(p.priority) : '–'} = area × (1 − ½·center dist) × (½ + ½·conf)<br />
              grade {g.grade ?? '–'} on {g.onEyes ? 'the eye band' : 'the head box'} · click to inspect
            </>)} />
        )}
        {on('regions') && <>
          <rect x={p.torso[0]} y={p.torso[1]} width={boxW(p.torso)} height={boxH(p.torso)} stroke="#a78bfa" {...stroke(1.25)} strokeDasharray="2 3"
            {...hv(`Torso · person #${i + 1}`, <>shoulders to hips, {boxW(p.torso)}×{boxH(p.torso)} px · Laplacian {fmt(p.sharp_torso)}</>)} />
          <rect x={p.head[0]} y={p.head[1]} width={boxW(p.head)} height={boxH(p.head)} stroke="#fbbf24" {...stroke(1.5)}
            strokeDasharray={p.head_src === 'box_top' ? '8 5' : undefined}
            {...hv(`Head box · person #${i + 1}`, <>placed from {p.head_src === 'keypoints' ? 'nose/eye/ear keypoints, sized from shoulder width' : 'the top of the person box (no confident head keypoints)'} · {boxW(p.head)}×{boxH(p.head)} px · Laplacian {fmt(p.sharp_head)}</>)} />
        </>}
        {on('eyes') && p.face && <>
          <rect x={p.face.search[0]} y={p.face.search[1]} width={boxW(p.face.search)} height={boxH(p.face.search)} stroke="#f9a8d4" {...stroke(1)} strokeDasharray="1 4" pointerEvents="none" />
          <rect x={p.face.box[0]} y={p.face.box[1]} width={boxW(p.face.box)} height={boxH(p.face.box)} stroke="#f472b6" {...stroke(1.25)}
            {...hv(`Face · person #${i + 1}`, <>YuNet face score {fmt(p.face.score)}, found inside the dotted search window (head box × 3). Its eye landmarks place the eye band.</>)} />
          {p.face.lm.map(([x, y], j) => (
            <circle key={j} cx={x} cy={y} r={3.5 * u} fill="#f472b6" stroke="#000" {...{ vectorEffect: 'non-scaling-stroke' }} strokeWidth={1}
              {...hv(`Face landmark: ${FACE_LM[j]}`, <>({x}, {y}) px · person #{i + 1}</>)} />
          ))}
        </>}
        {on('eyes') && p.eye && <>
          <rect x={p.eye[0]} y={p.eye[1]} width={boxW(p.eye)} height={boxH(p.eye)} stroke={gc} {...stroke(2)}
            {...hv(`Eye band · person #${i + 1}`, <>
              {boxW(p.eye)}×{boxH(p.eye)} px at native resolution, eyes from {p.eye_src === 'face' ? 'face landmarks' : 'pose keypoints'}<br />
              Laplacian {fmt(p.sharp_eye)} · FFT ratio {fmt(p.hf_eye)} → grade {g.onEyes ? g.grade ?? '–' : '– (not used)'}
            </>)} />
          {p.eyes && <line x1={p.eyes[0][0]} y1={p.eyes[0][1]} x2={p.eyes[1][0]} y2={p.eyes[1][1]} stroke={gc} {...stroke(1)} strokeDasharray="3 3" pointerEvents="none" />}
        </>}
        {on('skeleton') && kp && <>
          {SKELETON.map(([a, b]) => {
            const ok = kp[a][2] >= KP_MIN_CONF && kp[b][2] >= KP_MIN_CONF;
            return ok && <line key={`${a}-${b}`} x1={kp[a][0]} y1={kp[a][1]} x2={kp[b][0]} y2={kp[b][1]} stroke={c}
              {...stroke(primary ? 2 : 1.25)} opacity={0.35 + 0.55 * Math.min(kp[a][2], kp[b][2])} pointerEvents="none" />;
          })}
          {kp.map(([x, y, cf], j) => (
            <g key={j} {...hv(`${KP_NAMES[j]} · person #${i + 1}`, <>confidence {fmt(cf)} at ({x}, {y}) px{cf < KP_MIN_CONF ? ' · below 0.3, ignored' : j <= 4 ? ' · centers the head box' : j === 5 || j === 6 ? ' · sizes the head box, tops the torso' : j === 11 || j === 12 ? ' · bottoms the torso' : ''}</>)}>
              <circle cx={x} cy={y} r={10 * u} fill="transparent" />
              <circle cx={x} cy={y} r={(j <= 4 ? 4.5 : 4) * u} fill={cf >= KP_MIN_CONF ? c : 'none'} fillOpacity={0.3 + 0.7 * cf}
                stroke={cf >= KP_MIN_CONF ? '#000' : c} vectorEffect="non-scaling-stroke" strokeWidth={1} />
            </g>
          ))}
        </>}
        {on('people') && label(bx0, by0, `#${i + 1}${primary ? ' primary' : ''} ${g.grade != null ? `· g${g.grade}` : ''}`, c)}
      </g>
    );
  };

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="absolute inset-0 w-full h-full" onMouseLeave={() => setHover(null)}>
      <defs>
        <pattern id={hatch} width={12 * u} height={12 * u} patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
          <rect width={12 * u} height={12 * u} fill="rgba(0,0,0,.35)" />
          <line x1={0} y1={0} x2={0} y2={12 * u} stroke="rgba(255,255,255,.25)" strokeWidth={3 * u} />
        </pattern>
      </defs>
      {on('heatmap') && heat && (
        <image href={heat.img} x={0} y={0} width={heat.cover[0]} height={heat.cover[1]} preserveAspectRatio="none" style={{ imageRendering: 'pixelated' }} opacity={0.8} pointerEvents="none" />
      )}
      {on('mask') && (l.mask_boxes ?? people.map((p) => p.box)).map((b, j) => (
        <rect key={j} x={b[0]} y={b[1]} width={boxW(b)} height={boxH(b)} fill={`url(#${hatch})`} pointerEvents="none" />
      ))}
      {on('crop') && l.crop_box && <>
        <rect x={l.crop_box[0]} y={l.crop_box[1]} width={boxW(l.crop_box)} height={boxH(l.crop_box)} stroke="#fff" {...stroke(1.5)} strokeDasharray="10 6" pointerEvents="none" />
        {label(l.crop_box[0], l.crop_box[3] + fs * 1.4, 'model crop', '#fff')}
      </>}
      {on('people') && extra.map((b, j) => (
        <rect key={`x${j}`} x={b[0]} y={b[1]} width={boxW(b)} height={boxH(b)} stroke="#6b7280" {...stroke(1)} strokeDasharray="2 4"
          {...hv('Person (not ranked in the top 6)', <>Found and masked out of the background, but not stored with metrics.</>)} />
      ))}
      {people.map((p, i) => personLayer(p, i)).reverse() /* primary drawn last, on top */}
      {on('af') && l.af && l.af.points.map((pt) => {
        const act = l.af!.active.includes(pt.i);
        const [x0, y0, x1, y1] = pt.box;
        return (
          <g key={`af${pt.i}`} {...hv(`AF point ${pt.i}${l.af!.primary_point === pt.i ? ' · primary AF point' : ''}`, <>
            {pt.in_focus ? 'reported focus' : 'did not report focus'}{pt.selected ? ' · selected' : ''} · {l.af!.mode_name}
            {l.af!.user_placed ? ' (placed by the photographer)' : ' (the camera chose)'}<br />
            {boxW(pt.box)}×{boxH(pt.box)} px at ({x0}, {y0}){act ? ' · picks the primary subject' : ''}
          </>)}>
            <rect x={x0} y={y0} width={x1 - x0} height={y1 - y0} stroke={act ? '#ef4444' : '#fca5a5'}
              fill={act ? 'rgba(239,68,68,.18)' : 'transparent'} vectorEffect="non-scaling-stroke" strokeWidth={act ? 2 : 1.25}
              strokeDasharray={act ? undefined : '4 3'} />
          </g>
        );
      })}
    </svg>
  );
}

export function HoverBar({ hover, l }: { hover: Hover; l: LocalResult }) {
  const people = l.people ?? [];
  return (
    <div className="min-h-[2.5rem] text-xs">
      {hover
        ? <><div className="text-gray-200">{hover.title}</div><div className="text-gray-400">{hover.body}</div></>
        : <div className="text-gray-600">Hover a box or dot to see what the model found there; click a person to inspect their numbers.
          {people.length > 0 && !people[0].kp && ' Pose keypoints and face landmarks weren’t stored for this image yet: re-run the local stage (rescan) to see them.'}</div>}
    </div>
  );
}

/** The frame with the overlay, sized to the detail view. Clicking the photo (not a person) opens the viewer. */
export default function FrameOverlay({ id, l, onOpen, ...rest }: Omit<OverlayProps, 'setHover' | 'l' | 'zoom'> & {
  id: number; l: LocalResult | null | undefined; onOpen: () => void;
}) {
  const [hover, setHover] = useState<Hover>(null);
  if (!l) return <img src={frameUrl(id)} alt="" onClick={onOpen} className="w-full rounded-lg bg-gray-900 object-contain max-h-[60vh] cursor-zoom-in" />;
  return (
    <div className="space-y-1">
      <div onClick={onOpen} className="group relative mx-auto rounded-lg overflow-hidden bg-gray-900 cursor-zoom-in"
        style={{ aspectRatio: `${l.width} / ${l.height}`, width: `min(100%, calc(60vh * ${l.width / l.height}))` }}>
        <img src={frameUrl(id)} alt="" className="absolute inset-0 w-full h-full" />
        <OverlaySvg l={l} setHover={setHover} {...rest} />
        <span className="absolute right-2 top-2 rounded bg-black/60 px-1.5 text-xs text-gray-300 opacity-0 group-hover:opacity-100 pointer-events-none">⛶ click to enlarge</span>
      </div>
      <HoverBar hover={hover} l={l} />
    </div>
  );
}
