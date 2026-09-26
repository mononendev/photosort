import { fmtNum } from '../lib/format';

/** Previous / next over an offset-paged list; renders nothing when everything fits on one page. */
export default function Pager({ offset, limit, total, onPage, prev = '← prev', next = 'next →', className = '' }: {
  offset: number; limit: number; total: number; onPage: (offset: number) => void; prev?: string; next?: string; className?: string;
}) {
  if (total <= limit) return null;
  const btn = 'px-4 py-2 sm:px-3 sm:py-1 rounded bg-gray-800 hover:bg-gray-700 active:bg-gray-700 disabled:opacity-40';
  return (
    <div className={`flex items-center gap-3 text-sm ${className}`}>
      <button disabled={offset === 0} onClick={() => onPage(Math.max(0, offset - limit))} className={btn}>{prev}</button>
      <span className="text-gray-500 tabular-nums">{fmtNum(offset + 1)}–{fmtNum(Math.min(offset + limit, total))} of {fmtNum(total)}</span>
      <button disabled={offset + limit >= total} onClick={() => onPage(offset + limit)} className={btn}>{next}</button>
    </div>
  );
}
