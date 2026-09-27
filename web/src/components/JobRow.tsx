import { Link, useMatch, useNavigate } from 'react-router-dom';
import type { MouseEvent } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, isFinished, isLive } from '../api/client';
import type { Job, JobOptions } from '../api/client';
import { errMsg, fmtEta, fmtFinishAt } from '../lib/format';
import Progress from './Progress';
import Tip from './Tip';

const STATE_CLASS: Record<string, string> = {
  queued: 'text-gray-400', running: 'text-blue-300', preempting: 'text-amber-300', cancelling: 'text-amber-300',
  done: 'text-emerald-300', cancelled: 'text-gray-500', failed: 'text-red-400',
};

export default function JobRow({ job, compact }: { job: Job; compact?: boolean }) {
  const qc = useQueryClient();
  const cancel = useMutation({ mutationFn: () => api.cancelJob(job.id), onSuccess: () => qc.invalidateQueries({ queryKey: ['jobs'] }) });
  const override = useMutation({ mutationFn: () => api.overrideJob(job.id), onSuccess: () => qc.invalidateQueries({ queryKey: ['jobs'] }) });
  // Re-run = a new job over the same paths and options. "resume" picks up images the job didn't finish
  // (and retries errors); "re-analyze" redoes the local stage on everything (new metrics, new thresholds).
  const rerun = useMutation({
    mutationFn: (extra: JobOptions) => api.createJob(job.paths, { ...job.options, retry_errors: true, ...extra }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['jobs'] }),
  });
  const live = isLive(job);
  const finished = isFinished(job);
  const incomplete = finished && (job.state !== 'done' || job.errors > 0 || job.done < job.total);
  const ahead = job.state === 'running' && job.lane === 'ahead';
  // The whole card opens the job's details (except on that page), leaving its own links, buttons and
  // tooltips alone, and a drag-to-select of the paths doesn't count as a click.
  const navigate = useNavigate();
  const href = `/jobs/${job.id}`;
  const linked = !useMatch(href);
  const open = (e: MouseEvent) => {
    if (!linked || (e.target as HTMLElement).closest('a, button, [tabindex]') || window.getSelection()?.toString()) return;
    if (e.metaKey || e.ctrlKey || e.button === 1) window.open(href, '_blank');
    else if (e.button === 0) navigate(href);
  };
  return (
    <div onClick={open} onAuxClick={open}
      className={`rounded-lg border border-gray-800 bg-gray-900 p-3 ${linked ? 'cursor-pointer hover:border-gray-700' : ''}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-sm">
        <Link to={href} className="text-gray-500 hover:text-blue-300">#{job.id}</Link>
        <span className={`font-medium ${STATE_CLASS[job.state]}`}>{job.state}</span>
        <span className="text-gray-400">{job.stage}</span>
        {ahead && <Tip tip="Running its local stage ahead of time, while the job in front of it waits on the vision model. It goes back in the queue once local is done (or the model frees up) and resumes from there." className="text-xs text-gray-500">ahead</Tip>}
        <Link to={href} className="text-gray-300 truncate flex-1 min-w-[8rem] basis-40 sm:basis-0 hover:text-blue-300" title={`${job.paths.join('\n')}\n\nOpen job details`}>
          {job.paths.map((p) => p.split('/').slice(-2).join('/')).join(', ')}
        </Link>
        <Tip tip={<>Progress of the current stage ({job.stage === 'done' ? 'the last stage that had work' : job.stage}). The local stage counts images analyzed; the vlm stage counts images sent to the vision model. The note below keeps the local stage's summary.</>} className="text-gray-400 tabular-nums">{job.done}/{job.total}</Tip>
        {job.errors > 0 && <Tip tip="Images that failed in the local or vision stage, both added up. Filter Photos by status = error for the messages; resume retries them." className="text-red-400">{job.errors} err</Tip>}
        {live && job.rate ? <Tip tip={`Rate and time left in the ${job.stage} stage, since that stage started; the clock time is in your timezone.`} className="text-gray-500">{job.rate}/s · eta {fmtEta(job.eta_s)} · done {fmtFinishAt(job.eta_s)}</Tip> : null}
        {(job.state === 'queued' || ahead) && (
          <Tip plain tip="Run this job now. The running job pauses (images it finished stay done), goes back in the queue and resumes after this one.">
            <button onClick={() => override.mutate()} disabled={override.isPending}
              className="text-xs px-2.5 py-1 sm:px-2 sm:py-0.5 rounded bg-gray-800 hover:bg-gray-700 active:bg-gray-600 disabled:opacity-40">override</button>
          </Tip>
        )}
        {override.isError && <span className="text-xs text-red-400">{errMsg(override.error)}</span>}
        {(job.state === 'queued' || job.state === 'running' || job.state === 'preempting') && (
          <button onClick={() => cancel.mutate()} className="text-xs px-2 py-1 -my-1 rounded text-gray-400 hover:text-red-300 active:bg-gray-800">cancel</button>
        )}
        {finished && incomplete && (
          <Tip plain tip="Queue a new job with the same paths and options. It only processes images this job didn't finish and retries errors; finished images are left alone.">
            <button onClick={() => rerun.mutate({ rescan: false, revlm: false })} disabled={rerun.isPending}
              className="text-xs px-2.5 py-1 sm:px-2 sm:py-0.5 rounded bg-gray-800 hover:bg-gray-700 active:bg-gray-600 disabled:opacity-40">resume</button>
          </Tip>
        )}
        {finished && (
          <Tip plain tip="Queue a new job with the same paths and options, re-analyzing every image locally: new detections, eye bands, metrics and tiers with the current thresholds. Existing vision-model tags are kept (only untagged images go to the model) unless the job also re-tagged.">
            <button onClick={() => rerun.mutate({ rescan: true })} disabled={rerun.isPending}
              className="text-xs px-2.5 py-1 sm:px-2 sm:py-0.5 rounded bg-gray-800 hover:bg-gray-700 active:bg-gray-600 disabled:opacity-40">re-run</button>
          </Tip>
        )}
        {rerun.isSuccess && <span className="text-xs text-emerald-300">queued #{rerun.data.id}</span>}
        {rerun.isError && <span className="text-xs text-red-400">{errMsg(rerun.error)}</span>}
      </div>
      {(live || job.state === 'queued') && <Progress done={job.done} total={job.total} className="mt-2" />}
      {!compact && (
        <div className="mt-1 text-xs text-gray-500">
          {job.options.vlm === false ? 'local only' : 'local + vision model'}
          {job.options.skip_tier0 ? ' · skip tier 0' : ''}{job.options.rescan ? ' · re-analyze' : ''}{job.options.revlm && job.options.vlm !== false ? ' · re-tag' : ''}
          {job.message ? ` · ${job.message}` : ''}
        </div>
      )}
    </div>
  );
}
