import { TIER_CLASS, TIER_LABEL } from '../api/client';

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
