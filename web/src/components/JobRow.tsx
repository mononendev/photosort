import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { Job, JobOptions } from '../api/client';
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
  // Re-run = a new job over the same paths and options. "resume" picks up images the job didn't finish
  // (and retries errors); "re-analyze" redoes the local stage on everything (new metrics, new thresholds).
  const rerun = useMutation({
    mutationFn: (extra: JobOptions) => api.createJob(job.paths, { ...job.options, retry_errors: true, ...extra }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['jobs'] }),
  });
  const live = job.state === 'running' || job.state === 'cancelling';
  const finished = job.state === 'done' || job.state === 'cancelled' || job.state === 'failed';
  const incomplete = finished && (job.state !== 'done' || job.errors > 0 || job.done < job.total);
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
        {finished && incomplete && (
          <button onClick={() => rerun.mutate({ rescan: false })} disabled={rerun.isPending} title="New job, same paths and options: finish what's left and retry errors"
            className="text-xs px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40">resume</button>
        )}
        {finished && (
          <button onClick={() => rerun.mutate({ rescan: true })} disabled={rerun.isPending} title="New job, same paths and options, re-analyzing every image locally (vision-model results are kept)"
            className="text-xs px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700 disabled:opacity-40">re-run</button>
        )}
        {rerun.isSuccess && <span className="text-xs text-emerald-300">queued #{rerun.data.id}</span>}
        {rerun.isError && <span className="text-xs text-red-400">{(rerun.error as Error).message}</span>}
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
