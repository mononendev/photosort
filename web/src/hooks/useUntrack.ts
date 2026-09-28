import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { ImageFilters } from '../api/client';

/** Untrack: count what the filters (and paths) match, confirm, then forget those images. Resolves to a summary line,
 * or null when the user backs out. */
export function useUntrack(onDone: (msg: string) => void, onError: (e: unknown) => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ filters, paths }: { filters: ImageFilters; paths?: string[] }) => {
      const dry = await api.untrack(filters, paths, true);
      if (dry.untracked === 0) return `nothing to untrack${dry.kept ? ` (${dry.kept} rated/edited kept)` : ''}`;
      const kept = dry.kept ? `\n\n${dry.kept} photo(s) you rated, edited or have ground truth for stay tracked.` : '';
      if (!window.confirm(`Untrack ${dry.untracked} photo(s)? Their analysis, tags and cached previews are dropped; the files stay on disk, and a job over their folder registers them again.${kept}`)) return null;
      const res = await api.untrack(filters, paths);
      return `untracked ${res.untracked} photo(s)${res.kept ? `, kept ${res.kept} rated/edited` : ''}`;
    },
    onSuccess: (m) => {
      if (m === null) return;
      for (const key of ['images', 'tree', 'stats', 'image']) qc.invalidateQueries({ queryKey: [key] });
      onDone(m);
    },
    onError,
  });
}
