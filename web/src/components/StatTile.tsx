import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import Tip from './Tip';

/** A labelled number, optionally a link. `compact` is the denser size used in rows of job KPIs. */
export default function StatTile({ label, value, sub, tip, to, compact }: {
  label: string; value: ReactNode; sub?: ReactNode; tip?: ReactNode; to?: string; compact?: boolean;
}) {
  const body = (
    <div className={`h-full rounded-lg border border-gray-800 bg-gray-900 ${compact ? 'px-3 py-2' : 'p-3 sm:p-4'} ${to ? 'hover:border-gray-700 transition active:scale-[0.97] active:border-gray-600' : ''}`}>
      <div className={`${compact ? 'text-[11px]' : 'text-xs'} uppercase tracking-wide text-gray-500`}>{tip ? <Tip tip={tip}>{label}</Tip> : label}</div>
      <div className={`${compact ? 'text-xl' : 'text-2xl mt-1'} font-semibold tabular-nums text-gray-100`}>{value}</div>
      {sub && <div className={`text-xs text-gray-500 tabular-nums ${compact ? '' : 'mt-1'}`}>{sub}</div>}
    </div>
  );
  return to ? <Link to={to}>{body}</Link> : body;
}
