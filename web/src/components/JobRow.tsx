import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { Job } from '../api/client';
import Progress from './Progress';

const STATE_CLASS: Record<string, string> = {
  queued: 'text-gray-400', running: 'text-blue-300', cancelling: 'text-amber-300',
  done: 'text-emerald-300', cancelled: 'text-gray-500', failed: 'text-red-400',
};

function fmtEta(s: number | null | undefined): string {
  if (!s) return '';
  if (s < 90) return `${s}s`;
  if (s < 5400) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

export default function JobRow({ job, compact }: { job: Job; compact?: boolean }) {
  const qc = useQueryClient();
  const cancel = useMutation({ mutationFn: () => api.cancelJob(job.id), onSuccess: () => qc.invalidateQueries({ queryKey: ['jobs'] }) });
  const live = job.state === 'running' || job.state === 'cancelling';
  return (
    <div className="rounded-lg border border-gray-800 bg-gray-900 p-3">
      <div className="flex items-center gap-3 text-sm">
        <span className="text-gray-500">#{job.id}</span>
        <span className={`font-medium ${STATE_CLASS[job.state]}`}>{job.state}</span>
        <span className="text-gray-400">{job.stage}</span>
        <span className="text-gray-300 truncate flex-1" title={job.paths.join('\n')}>
          {job.paths.map((p) => p.split('/').slice(-2).join('/')).join(', ')}
        </span>
        <span className="text-gray-400 tabular-nums">{job.done}/{job.total}</span>
        {job.errors > 0 && <span className="text-red-400">{job.errors} err</span>}
        {live && job.rate ? <span className="text-gray-500">{job.rate}/s · eta {fmtEta(job.eta_s)}</span> : null}
        {(job.state === 'queued' || job.state === 'running') && (
          <button onClick={() => cancel.mutate()} className="text-xs text-gray-400 hover:text-red-300">cancel</button>
        )}
      </div>
      {(live || job.state === 'queued') && <Progress done={job.done} total={job.total} className="mt-2" />}
      {!compact && (
        <div className="mt-1 text-xs text-gray-500">
          {job.options.vlm === false ? 'local only' : 'local + vision model'}
          {job.options.skip_tier0 ? ' · skip tier 0' : ''}{job.options.rescan ? ' · re-analyze' : ''}
          {job.message ? ` · ${job.message}` : ''}
        </div>
      )}
    </div>
  );
}
