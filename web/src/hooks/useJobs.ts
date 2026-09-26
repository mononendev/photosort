import { useEffect, useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, isBusy } from '../api/client';

/** The job list, polled quickly while any job is queued or running and slowly otherwise. Every component shares
 * this one query, so the interval is the same wherever it is used. */
export function useJobs() {
  return useQuery({ queryKey: ['jobs'], queryFn: api.jobs, refetchInterval: (q) => (q.state.data?.some(isBusy) ? 2000 : 15000) });
}

/** Whether any job is queued or running: views of data that jobs change (stats, folders, photos) poll only then. */
export function useBusy(): boolean {
  return !!useJobs().data?.some(isBusy);
}

/** When a job ends, refresh the views that only poll while busy so they show its final results. Keyed on the newest
 * finish time rather than on busy -> idle, so a job that started and ended between two idle polls still counts. */
export function useRefreshOnJobEnd() {
  const last = Math.max(0, ...(useJobs().data ?? []).map((j) => j.finished ?? 0));
  const qc = useQueryClient();
  const seen = useRef(last);
  useEffect(() => {
    if (last > seen.current) for (const key of ['stats', 'tree', 'images', 'image']) qc.invalidateQueries({ queryKey: [key] });
    seen.current = last;
  }, [last, qc]);
}
