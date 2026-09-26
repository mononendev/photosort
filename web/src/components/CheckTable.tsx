import { fmt } from '../lib/format';

/** t holds the tier-1, tier-2 and tier-3 cuts, in that order. */
export type Check = { label: string; value: number | null | undefined; t: [number, number, number] };

/** Metric values against the tier-3 / tier-2 / tier-1 thresholds, with pass/fail marks. */
export default function CheckTable({ checks }: { checks: Check[] }) {
  const mark = (ok: boolean) => (ok ? <span className="text-emerald-300">✓</span> : <span className="text-red-300">✗</span>);
  return (
    <table className="my-1 font-mono text-[11px]">
      <thead><tr className="text-gray-500"><td className="pr-2">metric</td><td className="pr-2">value</td>{[3, 2, 1].map((n) => <td key={n} className="pr-2">tier {n} ≥</td>)}</tr></thead>
      <tbody>{checks.map((c) => (
        <tr key={c.label}>
          <td className="pr-2 text-gray-400">{c.label}</td><td className="pr-2">{fmt(c.value)}</td>
          {[2, 1, 0].map((k) => <td key={k} className="pr-2">{fmt(c.t[k])} {c.value != null && mark(c.value >= c.t[k])}</td>)}
        </tr>
      ))}</tbody>
    </table>
  );
}
