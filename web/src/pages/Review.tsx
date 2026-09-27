import { useCallback, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import ImageDetail from '../components/ImageDetail';

/** Every needs-review photo you haven't rated yet, one at a time in the detail view. Rating one drops it from the queue. */
export default function Review() {
  const { data, isLoading } = useQuery({
    queryKey: ['images', 'review-queue'],
    queryFn: () => api.images({ review: true, reviewed: false, sort: 'path', limit: 500 }),
  });
  const items = useMemo(() => (data?.items ?? []).filter((x) => x.id !== null), [data]);
  const [picked, setPicked] = useState<number | null>(null);
  // Follow the pick while it's still in the queue; once it's rated and gone, fall back to the first one left.
  const i = Math.max(0, items.findIndex((x) => x.id === picked));
  const id = items[i]?.id ?? null;
  const nav = useCallback((dir: 1 | -1) => {
    const nx = items[i + dir];
    if (nx?.id) setPicked(nx.id);
  }, [items, i]);

  if (isLoading) return <p className="text-sm text-gray-500">loading…</p>;
  if (id === null) return <p className="text-sm text-gray-500">Nothing left to review.</p>;
  return (
    <div className="space-y-2">
      <div className="text-xs text-gray-500">{i + 1} of {data?.total ?? items.length} to review</div>
      <ImageDetail id={id} onNav={nav} />
    </div>
  );
}
