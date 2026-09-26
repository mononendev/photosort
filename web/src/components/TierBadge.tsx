import { RATINGS, TIER_CLASS, TIER_LABEL } from '../api/client';

export function TierBadge({ tier, small }: { tier: number | null | undefined; small?: boolean }) {
  if (tier === null || tier === undefined) {
    return <span className={`rounded border border-gray-700 bg-gray-800 text-gray-400 ${small ? 'px-1 text-[10px]' : 'px-2 py-0.5 text-xs'}`}>n/a</span>;
  }
  return (
    <span className={`rounded border ${TIER_CLASS[tier]} ${small ? 'px-1 text-[10px]' : 'px-2 py-0.5 text-xs'}`} title={TIER_LABEL[tier]}>
      {small ? `F${tier}` : `focus ${tier}: ${TIER_LABEL[tier]}`}
    </span>
  );
}

/** Your cull rating (0-2 focus, ★ banger), only once you've reviewed the photo. */
export function RatingBadge({ rating }: { rating: number | null | undefined }) {
  const r = rating == null ? undefined : RATINGS[rating];
  if (!r) return null;
  return <span className={`rounded border px-1 text-[10px] ${r.cls}`} title={`your call: ${r.label}`}>{r.short}</span>;
}

export function Stars({ n }: { n: number | null | undefined }) {
  if (!n) return null;
  return <span className="text-amber-300 text-xs tracking-tight">{'★'.repeat(n)}<span className="text-gray-600">{'★'.repeat(5 - n)}</span></span>;
}

export function StatusDot({ status }: { status: string }) {
  const c: Record<string, string> = {
    untracked: 'bg-gray-600', pending: 'bg-gray-400', analyzed: 'bg-blue-400', tagged: 'bg-emerald-400', error: 'bg-red-500',
  };
  return <span className={`inline-block w-2 h-2 rounded-full ${c[status] ?? 'bg-gray-600'}`} title={status} />;
}

export function LrBadge({ rating, label }: { rating?: number | null; label?: string | null }) {
  if (!rating && !label) return null;
  const colors: Record<string, string> = { Red: 'bg-red-700', Yellow: 'bg-yellow-600', Green: 'bg-green-700', Blue: 'bg-blue-700', Purple: 'bg-purple-700' };
  return (
    <span className="inline-flex items-center gap-1 text-[10px] text-gray-300" title="your Lightroom rating / label">
      <span className="rounded bg-gray-800 px-1">LR {rating ? '★'.repeat(rating) : '–'}</span>
      {label && <span className={`w-2 h-2 rounded-full ${colors[label] ?? 'bg-gray-500'}`} title={label} />}
    </span>
  );
}
