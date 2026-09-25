export type Check = { label: string; value: number | null | undefined; t2: number; t1: number };

// eslint-disable-next-line react-refresh/only-export-components
export const fmt = (v: number | null | undefined) => (v === null || v === undefined ? '–' : v < 0.01 ? v.toFixed(4) : v.toFixed(3));

/** Metric values against the tier-2 / tier-1 thresholds, with pass/fail marks. */
export default function CheckTable({ checks }: { checks: Check[] }) {
  const mark = (ok: boolean) => (ok ? <span className="text-emerald-300">✓</span> : <span className="text-red-300">✗</span>);
  return (
    <table className="my-1 font-mono text-[11px]">
      <thead><tr className="text-gray-500"><td className="pr-2">metric</td><td className="pr-2">value</td><td className="pr-2">tier 2 ≥</td><td>tier 1 ≥</td></tr></thead>
      <tbody>{checks.map((c) => (
        <tr key={c.label}>
          <td className="pr-2 text-gray-400">{c.label}</td><td className="pr-2">{fmt(c.value)}</td>
          <td className="pr-2">{fmt(c.t2)} {c.value != null && mark(c.value >= c.t2)}</td>
          <td>{fmt(c.t1)} {c.value != null && mark(c.value >= c.t1)}</td>
        </tr>
      ))}</tbody>
    </table>
  );
}
