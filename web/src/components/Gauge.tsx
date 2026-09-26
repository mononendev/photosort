import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import Tip from './Tip';
import { fmt } from '../lib/format';
import { TIER_COLOR } from '../api/client';

/**
 * A metric on a log axis with its tier zones (red, orange, yellow, green), cut at the tier-1, tier-2 and tier-3
 * thresholds in `thr`. Without thresholds it is a plain bar against `ref` values (other regions of the same
 * person, for comparison).
 */
export default function Gauge({ label, value, thr, refs = [], tip, note }: {
  label: string; value: number | null | undefined; thr?: [number, number, number];
  refs?: { label: string; value: number | null | undefined }[]; tip?: ReactNode; note?: ReactNode;
}) {
  const known = [value, ...(thr ?? []), ...refs.map((r) => r.value)].filter((v): v is number => v != null && v > 0);
  const lo = Math.log10(Math.min(...known, 1e-3) / 2.5);
  const hi = Math.log10(Math.max(...known, 1e-3) * 2.5);
  const x = (v: number) => `${Math.max(0, Math.min(100, ((Math.log10(Math.max(v, 1e-9)) - lo) / (hi - lo)) * 100))}%`;
  const hasThr = !!thr && thr.every((t) => t != null);
  const color = value == null || !hasThr ? '#e5e7eb' : TIER_COLOR[[3, 2, 1].find((n) => value >= thr[n - 1]) ?? 0];
  // Axis labels that would overprint go on separate rows. Labels are centred on their mark, 9px mono ≈ 5.5px/char.
  const bar = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(300);
  useLayoutEffect(() => {
    const el = bar.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const halfW = (label: string) => label.length * 2.75 + 4;
  const rowEnds: number[] = [];
  const marks = hasThr ? thr.map((t, k) => ({ label: `t${k + 1} ${fmt(t)}`, value: t })) : refs;
  const axisLabels = marks.filter((r): r is { label: string; value: number } => r.value != null)
    .sort((a, b) => a.value - b.value)
    .map((r) => {
      const at = (parseFloat(x(r.value)) / 100) * width;
      let row = rowEnds.findIndex((end) => at - halfW(r.label) >= end);
      if (row < 0) { row = rowEnds.length; rowEnds.push(0); }
      rowEnds[row] = at + halfW(r.label);
      return { ...r, row };
    });
  return (
    <div className="text-[11px]">
      <div className="flex justify-between gap-2">
        <span className="text-gray-400">{tip ? <Tip tip={tip}>{label}</Tip> : label}</span>
        <span className="font-mono" style={{ color }}>{fmt(value)}{note ? <span className="text-gray-500"> {note}</span> : null}</span>
      </div>
      <div ref={bar} className="relative h-3 mt-0.5 rounded bg-gray-800 overflow-hidden">
        {hasThr && <>
          <div className="absolute inset-y-0 left-0 bg-red-900/60" style={{ width: x(thr[0]) }} />
          <div className="absolute inset-y-0 bg-orange-900/60" style={{ left: x(thr[0]), width: `calc(${x(thr[1])} - ${x(thr[0])})` }} />
          <div className="absolute inset-y-0 bg-yellow-900/60" style={{ left: x(thr[1]), width: `calc(${x(thr[2])} - ${x(thr[1])})` }} />
          <div className="absolute inset-y-0 right-0 bg-emerald-900/60" style={{ left: x(thr[2]) }} />
        </>}
        {refs.map((r) => r.value != null && (
          <div key={r.label} title={`${r.label} ${fmt(r.value)}`} className="absolute inset-y-0 w-px bg-gray-400/70" style={{ left: x(r.value) }} />
        ))}
        {value != null && <div className="absolute inset-y-0 w-1 -ml-0.5 rounded" style={{ left: x(value), background: color, boxShadow: '0 0 0 1px #000' }} />}
      </div>
      {(hasThr || refs.length > 0) && (
        <div className="relative text-[9px] leading-3 text-gray-500 font-mono" style={{ height: `${Math.max(1, rowEnds.length) * 0.75}rem` }}>
          {axisLabels.map((r) => <span key={r.label} className="absolute -translate-x-1/2 whitespace-nowrap" style={{ left: x(r.value), top: `${r.row * 0.75}rem` }}>{r.label}</span>)}
        </div>
      )}
    </div>
  );
}
