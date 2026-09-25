import { useState } from 'react';
import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api, thumbUrl } from '../api/client';
import type { ActiveItem, JobDetail as JobDetailT, JobItem, JobStage, StageStats } from '../api/client';
import JobRow from '../components/JobRow';
import ImageDetail from '../components/ImageDetail';
import { TierBadge, Stars } from '../components/TierBadge';
import Tip from '../components/Tip';

type StageName = 'scan' | 'local' | 'vlm';
const STAGES: StageName[] = ['scan', 'local', 'vlm'];
const STAGE_LABEL: Record<StageName, string> = { scan: 'Scan', local: 'Local stage', vlm: 'Vision model' };

function fmtDur(s: number | null | undefined): string {
  if (s === null || s === undefined || !isFinite(s)) return '–';
  if (s < 10) return `${s.toFixed(1)}s`;
  if (s < 90) return `${Math.round(s)}s`;
  if (s < 5400) return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
  return `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m`;
}
const fmtNum = (n: number | null | undefined) => (n === null || n === undefined ? '–' : n.toLocaleString());
const fmtK = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}k` : String(n));
const fmtTime = (t: number | null | undefined) => (t ? new Date(t * 1000).toLocaleString() : '–');
const fmtClock = (t: number) => new Date(t * 1000).toLocaleTimeString();

export default function JobDetail() {
  const id = Number(useParams().id);
  const { data: job, error } = useQuery({
    queryKey: ['job-detail', id], queryFn: () => api.jobDetail(id),
    refetchInterval: (q) => (isLive(q.state.data) ? 1000 : 5000),
  });
  const [open, setOpen] = useState<number | null>(null);
  if (error) return <p className="text-sm text-red-400">{(error as Error).message}</p>;
  if (!job) return <p className="text-sm text-gray-500">Loading…</p>;
  const live = isLive(job);
  const vlmStage = job.stages.vlm;

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-3">
        <Link to="/jobs" className="text-sm text-gray-400 hover:text-gray-100">← Jobs</Link>
        <h1 className="text-lg font-semibold">Job #{job.id}</h1>
      </div>
      <JobRow job={job} />

      <StageStrip job={job} />
      <Kpis job={job} />
      {(live || job.active.length > 0) && <InFlight items={job.active} onOpen={setOpen} />}
      <Charts job={job} />
      <Items jobId={job.id} live={live} backend={vlmStage?.backend} model={vlmStage?.model} onOpen={setOpen} />
      <Inputs job={job} />

      {open !== null && <ImageDetail id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function isLive(j: JobDetailT | undefined) {
  return !!j && (j.state === 'running' || j.state === 'cancelling');
}

function Section({ title, tip, right, children }: { title: string; tip?: ReactNode; right?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-gray-800 bg-gray-900 p-4">
      <div className="flex items-center gap-3 mb-3">
        <h2 className="text-sm font-semibold text-gray-200">{tip ? <Tip tip={tip}>{title}</Tip> : title}</h2>
        <div className="ml-auto flex items-center gap-2">{right}</div>
      </div>
      {children}
    </section>
  );
}

// ---- stage strip -----------------------------------------------------------------

function StageStrip({ job }: { job: JobDetailT }) {
  const now = job.now;
  if (!Object.keys(job.stages).length) {
    return <p className="text-xs text-gray-500">No stage timings recorded for this job (it ran before they were kept, or hasn't started).</p>;
  }
  return (
    <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
      {STAGES.map((name) => {
        const st = job.stages[name];
        const current = job.stage === name && isLive(job);
        const dur = st ? (st.finished ?? (current ? now : undefined)) : undefined;
        return (
          <div key={name} className={`rounded-lg border p-3 ${current ? 'border-blue-600 bg-blue-950/30' : 'border-gray-800 bg-gray-900'} ${st ? '' : 'opacity-50'}`}>
            <div className="flex items-center gap-2 text-sm">
              {current && <span className="inline-block w-2 h-2 rounded-full bg-blue-400 animate-pulse" />}
              <span className="font-medium">{STAGE_LABEL[name]}</span>
              <span className="ml-auto text-xs text-gray-400 tabular-nums">{st && dur ? fmtDur(dur - st.started) : st ? '' : name === 'vlm' && job.options.vlm === false ? 'off' : name === 'vlm' && job.state === 'done' ? 'nothing to tag' : 'not reached'}</span>
            </div>
            {st && <StageLine name={name} st={current ? { ...st, done: job.done } : st} />}
          </div>
        );
      })}
    </div>
  );
}

function StageLine({ name, st }: { name: StageName; st: JobStage }) {
  const bits: string[] = [];
  if (name === 'scan') bits.push(`${fmtNum(st.files)} image files found`);
  if (name !== 'scan') {
    bits.push(st.total ? `${fmtNum(st.done ?? 0)}/${fmtNum(st.total)} images` : 'nothing new');
    if (st.errors) bits.push(`${st.errors} errors`);
  }
  if (name === 'local') bits.push(`${st.workers ?? '?'} workers`, st.device ? `on ${st.device}` : '');
  if (name === 'vlm') bits.push(`${st.backend}/${st.model}`, `×${st.concurrency ?? 1} concurrent`);
  return <div className="mt-1 text-xs text-gray-400">{bits.filter(Boolean).join(' · ')}</div>;
}

// ---- KPIs --------------------------------------------------------------------------

function Kpi({ label, value, sub, tip }: { label: string; value: ReactNode; sub?: ReactNode; tip?: ReactNode }) {
  return (
    <div className="rounded-lg border border-gray-800 bg-gray-900 px-3 py-2">
      <div className="text-[11px] uppercase tracking-wide text-gray-500">{tip ? <Tip tip={tip}>{label}</Tip> : label}</div>
      <div className="text-xl font-semibold tabular-nums text-gray-100">{value}</div>
      {sub && <div className="text-xs text-gray-500 tabular-nums">{sub}</div>}
    </div>
  );
}

function Kpis({ job }: { job: JobDetailT }) {
  const stageName = (job.stage === 'local' || job.stage === 'vlm' ? job.stage : job.stats.vlm ? 'vlm' : 'local') as 'local' | 'vlm';
  const s: StageStats | undefined = job.stats[stageName];
  const v = job.stats.vlm;
  const live = isLive(job);
  const rate = (live ? s?.recent_rate : null) ?? s?.rate ?? null;
  const eta = live && rate ? (job.total - job.done) / rate : null;
  const elapsed = job.started ? (job.finished ?? job.now) - job.started : null;
  return (
    <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2">
      <Kpi label="Progress" value={job.total ? `${Math.round((job.done / job.total) * 100)}%` : '–'}
        sub={`${fmtNum(job.done)}/${fmtNum(job.total)} · ${stageName}`} />
      <Kpi label={live ? 'ETA' : 'Elapsed'} value={live ? fmtDur(eta) : fmtDur(elapsed)}
        sub={live ? `elapsed ${fmtDur(elapsed)}` : job.finished ? `finished ${fmtClock(job.finished)}` : undefined}
        tip="Remaining images in the current stage divided by its recent rate (last 20 images). The vision stage can still follow the local one." />
      <Kpi label="Images/min" value={rate ? (rate * 60).toFixed(1) : '–'}
        sub={s ? `avg ${fmtDur(s.avg_s)}/image · p95 ${fmtDur(s.p95_s)}` : undefined}
        tip={<>Throughput of the {stageName} stage, over the last 20 images while running and the whole stage otherwise. Per-image time is wall time for one image; with several workers, images overlap.</>} />
      <Kpi label="Decode tok/s" value={v?.recent_tok_s ?? v?.tok_s ?? '–'}
        sub={v?.tok_s ? `stage avg ${v.tok_s}` : 'vision stage only'}
        tip="Output tokens per second of model decode time, as the model server reports it (Ollama eval_count / eval_duration). The big number is the last 10 images." />
      <Kpi label="Tokens in / out" value={v ? `${fmtK(v.tokens_in)} / ${fmtK(v.tokens_out)}` : '–'}
        sub={v?.avg_in ? `avg ${fmtNum(v.avg_in)} in · ${fmtNum(v.avg_out)} out` : undefined}
        tip="Prompt (images + text) and generated tokens across every image the vision model finished in this job." />
      <Kpi label="Prefill / decode" value={v?.avg_prefill_s != null ? `${v.avg_prefill_s}s / ${v.avg_decode_s}s` : '–'}
        sub={job.errors ? <span className="text-red-400">{job.errors} errors</span> : 'per image, average'}
        tip="Average time the model spends reading the prompt (prefill: images and text) versus writing the JSON (decode), per image." />
    </div>
  );
}

// ---- in flight ------------------------------------------------------------------------

function InFlight({ items, onOpen }: { items: ActiveItem[]; onOpen: (id: number) => void }) {
  return (
    <Section title={`Processing now (${items.length})`}
      tip="Images a worker has picked up and not finished yet. Elapsed counts from when that worker started on it.">
      {items.length === 0 ? <p className="text-xs text-gray-500">Between images…</p> : (
        <div className="grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-6 gap-2">
          {items.map((a) => (
            <button key={a.id} onClick={() => onOpen(a.id)} className="text-left rounded-md border border-gray-800 bg-gray-950 overflow-hidden hover:border-gray-600">
              <div className="aspect-[3/2] bg-gray-800 flex items-center justify-center">
                {a.has_thumb ? <img src={thumbUrl(a.id)} alt="" className="w-full h-full object-cover" />
                  : <span className="text-[10px] text-gray-500 animate-pulse">analyzing…</span>}
              </div>
              <div className="px-2 py-1">
                <div className="text-xs truncate" title={a.rel}>{a.name}</div>
                <div className="text-[10px] text-gray-500 tabular-nums">{a.stage} · {fmtDur(a.elapsed)}</div>
              </div>
            </button>
          ))}
        </div>
      )}
    </Section>
  );
}

// ---- charts --------------------------------------------------------------------------

function Charts({ job }: { job: JobDetailT }) {
  const have = (['local', 'vlm'] as const).filter((st) => job.series.some((p) => p.stage === st));
  const [pick, setPick] = useState<'local' | 'vlm' | null>(null);
  if (!have.length) return null;
  const stage = pick && have.includes(pick) ? pick : (job.stage === 'local' || job.stage === 'vlm') && have.includes(job.stage) ? job.stage : have[have.length - 1];
  const pts = job.series.filter((p) => p.stage === stage);
  const tok = pts.filter((p) => p.tok_s != null);
  return (
    <Section title="Per-image timing" tip={`The last ${job.series.length} images this job finished, oldest on the left. Failed images are red.`}
      right={have.length > 1 && have.map((st) => (
        <button key={st} onClick={() => setPick(st)}
          className={`text-xs px-2 py-0.5 rounded ${st === stage ? 'bg-gray-700 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-100'}`}>{st}</button>
      ))}>
      <div className={`grid gap-4 ${tok.length ? 'lg:grid-cols-2' : ''}`}>
        <BarChart title="Seconds per image" values={pts.map((p) => p.s)} errs={pts.map((p) => p.err)}
          label={(i) => `${fmtClock(pts[i].t)} · ${fmtDur(pts[i].s)}${pts[i].err ? ' · failed' : ''}`} unit="s" />
        {tok.length > 0 && (
          <BarChart title="Decode tokens/s" values={tok.map((p) => p.tok_s ?? 0)} errs={tok.map((p) => p.err)}
            label={(i) => `${fmtClock(tok[i].t)} · ${tok[i].tok_s} tok/s · ${tok[i].out ?? '?'} tokens`} unit="" />
        )}
      </div>
    </Section>
  );
}

function BarChart({ title, values, errs, label, unit }: { title: string; values: number[]; errs: boolean[]; label: (i: number) => string; unit: string }) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 600, H = 120, n = values.length;
  const max = Math.max(...values, 0.001);
  const bw = W / Math.max(n, 30);
  const gap = bw > 4 ? 2 : 0;
  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const i = Math.floor(((e.clientX - r.left) / r.width) * W / bw);
    setHover(i >= 0 && i < n ? i : null);
  };
  return (
    <div>
      <div className="flex text-xs text-gray-400 mb-1">
        <span>{title}</span>
        <span className="ml-auto tabular-nums text-gray-300 h-4">{hover !== null ? label(hover) : ''}</span>
      </div>
      <div className="flex gap-2">
        <div className="flex flex-col justify-between text-[10px] text-gray-500 tabular-nums text-right w-8">
          <span>{max < 10 ? max.toFixed(1) : Math.round(max)}{unit}</span><span>0</span>
        </div>
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="flex-1 h-28" onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
          <line x1={0} x2={W} y1={H - 0.5} y2={H - 0.5} className="stroke-gray-700" strokeWidth={1} vectorEffect="non-scaling-stroke" />
          {values.map((v, i) => {
            const h = Math.max(1, (v / max) * (H - 4));
            return <rect key={i} x={i * bw + gap / 2} y={H - h} width={Math.max(0.5, bw - gap)} height={h} rx={bw > 6 ? 2 : 0}
              className={errs[i] ? 'fill-red-500' : hover === i ? 'fill-blue-300' : 'fill-blue-500'} />;
          })}
        </svg>
      </div>
    </div>
  );
}

// ---- items ------------------------------------------------------------------------------

function Items({ jobId, live, backend, model, onOpen }: { jobId: number; live: boolean; backend?: string; model?: string; onOpen: (id: number) => void }) {
  const [stage, setStage] = useState<string>('');
  const [errors, setErrors] = useState(false);
  const [offset, setOffset] = useState(0);
  const [expanded, setExpanded] = useState<number | null>(null);
  const limit = 50;
  const { data } = useQuery({
    queryKey: ['job-items', jobId, stage, errors, offset],
    queryFn: () => api.jobItems(jobId, { stage: stage || undefined, errors: errors || undefined, offset, limit }),
    refetchInterval: live && offset === 0 ? 2000 : false,
  });
  const filterBtn = (label: string, on: boolean, click: () => void) => (
    <button onClick={() => { click(); setOffset(0); }}
      className={`text-xs px-2 py-0.5 rounded ${on ? 'bg-gray-700 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-100'}`}>{label}</button>
  );
  return (
    <Section title={`Processed images${data ? ` (${fmtNum(data.total)})` : ''}`}
      tip="Every image this job finished, newest first, with what each stage produced. Click a row for the vision model's input and output. Results shown are the image's current ones, so a later job or a manual override can have changed them."
      right={<>
        {filterBtn('all', stage === '', () => setStage(''))}
        {filterBtn('local', stage === 'local', () => setStage('local'))}
        {filterBtn('vision', stage === 'vlm', () => setStage('vlm'))}
        {filterBtn('errors only', errors, () => setErrors(!errors))}
      </>}>
      {!data?.items.length ? <p className="text-xs text-gray-500">{data ? 'Nothing yet.' : 'Loading…'}</p> : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-gray-500 text-left">
              <tr className="border-b border-gray-800">
                <th className="py-1 pr-2 font-normal" />
                <th className="py-1 pr-2 font-normal">image</th>
                <th className="py-1 pr-2 font-normal">stage</th>
                <th className="py-1 pr-2 font-normal">finished</th>
                <th className="py-1 pr-2 font-normal text-right">time</th>
                <th className="py-1 pr-2 font-normal text-right">tokens in→out</th>
                <th className="py-1 pr-2 font-normal text-right">tok/s</th>
                <th className="py-1 font-normal">result</th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((it) => (
                <ItemRows key={it.id} it={it} open={expanded === it.id} backend={backend} model={model}
                  onToggle={() => setExpanded(expanded === it.id ? null : it.id)} onOpen={onOpen} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data && data.total > limit && (
        <div className="flex items-center gap-2 mt-3 text-xs text-gray-400">
          <button disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))} className="px-2 py-0.5 rounded bg-gray-800 disabled:opacity-40">newer</button>
          <span className="tabular-nums">{offset + 1}–{Math.min(offset + limit, data.total)} of {fmtNum(data.total)}</span>
          <button disabled={offset + limit >= data.total} onClick={() => setOffset(offset + limit)} className="px-2 py-0.5 rounded bg-gray-800 disabled:opacity-40">older</button>
        </div>
      )}
    </Section>
  );
}

function ItemRows({ it, open, backend, model, onToggle, onOpen }: {
  it: JobItem; open: boolean; backend?: string; model?: string; onToggle: () => void; onOpen: (id: number) => void;
}) {
  return (
    <>
      <tr onClick={onToggle} className={`border-b border-gray-800/60 cursor-pointer hover:bg-gray-800/40 ${open ? 'bg-gray-800/40' : ''}`}>
        <td className="py-1 pr-2 w-14"><img src={thumbUrl(it.image_id)} alt="" loading="lazy" className="w-12 h-8 object-cover rounded bg-gray-800" /></td>
        <td className="py-1 pr-2 max-w-[16rem] truncate" title={it.rel ?? ''}>{it.name ?? `#${it.image_id}`}</td>
        <td className="py-1 pr-2 text-gray-400">{it.stage === 'vlm' ? 'vision' : 'local'}</td>
        <td className="py-1 pr-2 text-gray-400 tabular-nums">{fmtClock(it.finished)}</td>
        <td className="py-1 pr-2 text-right tabular-nums">{fmtDur(it.seconds)}</td>
        <td className="py-1 pr-2 text-right tabular-nums text-gray-400">{it.usage ? `${fmtNum(it.usage.in)}→${fmtNum(it.usage.out)}` : ''}</td>
        <td className="py-1 pr-2 text-right tabular-nums text-gray-400">{it.usage?.tok_s ?? ''}</td>
        <td className="py-1"><ItemResult it={it} /></td>
      </tr>
      {open && (
        <tr className="border-b border-gray-800">
          <td colSpan={8} className="p-3 bg-gray-950/60">
            <ItemIO it={it} backend={backend} model={model} onOpen={onOpen} />
          </td>
        </tr>
      )}
    </>
  );
}

function ItemResult({ it }: { it: JobItem }) {
  if (it.error) return <span className="text-red-400 line-clamp-1" title={it.error}>{it.error}</span>;
  if (it.stage === 'vlm' && it.vlm) {
    return (
      <span className="flex items-center gap-2 min-w-0">
        <TierBadge tier={it.vlm.focus_tier} small /><Stars n={it.vlm.quality_score} />
        {it.vlm.keeper && <span className="text-emerald-300">keeper</span>}
        <span className="text-gray-400 truncate">{it.vlm.primary_subject} · {it.vlm.composition}</span>
      </span>
    );
  }
  if (it.local) {
    return (
      <span className="flex items-center gap-2 min-w-0">
        <TierBadge tier={it.local.local_tier} small />
        <span className="text-gray-400 truncate">
          {it.local.n_people} {it.local.n_people === 1 ? 'person' : 'people'}
          {it.local.primary_eye_sharp != null ? ` · eye ${it.local.primary_eye_sharp}` : it.local.primary_head_sharp != null ? ` · head ${it.local.primary_head_sharp}` : ''}
        </span>
      </span>
    );
  }
  return <span className="text-gray-500">no result stored</span>;
}

function ItemIO({ it, backend, model, onOpen }: { it: JobItem; backend?: string; model?: string; onOpen: (id: number) => void }) {
  const isVlm = it.stage === 'vlm';
  const { data: req, error: reqErr } = useQuery({
    queryKey: ['vlm-request', it.image_id, backend, model], enabled: isVlm,
    queryFn: () => api.vlmRequest(it.image_id, backend, model), staleTime: 60_000,
  });
  const { data: img } = useQuery({ queryKey: ['image', it.image_id], queryFn: () => api.image(it.image_id) });
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3 text-xs">
        <span className="text-gray-300 truncate">{it.rel}</span>
        <span className="text-gray-500 tabular-nums">started {fmtClock(it.started)} · {fmtDur(it.seconds)}</span>
        <button onClick={() => onOpen(it.image_id)} className="ml-auto px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700">open photo</button>
      </div>
      {it.error && <pre className="text-xs text-red-300 whitespace-pre-wrap break-all rounded bg-red-950/30 border border-red-900 p-2">{it.error}</pre>}
      <div className="grid lg:grid-cols-2 gap-4">
        <div className="space-y-2 min-w-0">
          <h3 className="text-xs font-semibold text-gray-300">Input</h3>
          {isVlm ? (
            req ? (
              <>
                <div className="flex gap-2">
                  {req.images.map((im) => (
                    <figure key={im.label} className="w-1/2">
                      <img src={im.url} alt={im.label} className="w-full rounded bg-gray-800" />
                      <figcaption className="text-[10px] text-gray-500 mt-0.5">{im.label} · {(im.bytes / 1024).toFixed(0)} KB</figcaption>
                    </figure>
                  ))}
                </div>
                <Pre label="Detector context (user message)">{req.context}</Pre>
                <Collapse label="System prompt"><Pre>{req.system}</Pre></Collapse>
                {req.request && <Collapse label={`Full ${req.backend} request (${req.model})`}><Pre>{JSON.stringify(req.request, null, 2)}</Pre></Collapse>}
                {req.build_error && <p className="text-[11px] text-amber-300">Couldn't build the {req.backend} request here: {req.build_error}</p>}
                <p className="text-[10px] text-gray-500">Rebuilt from the current cache and config, so it matches what was sent unless either changed since.</p>
              </>
            ) : reqErr ? <p className="text-xs text-red-400">{(reqErr as Error).message}</p> : <p className="text-xs text-gray-500">Loading…</p>
          ) : (
            <p className="text-xs text-gray-400">The original file, <span className="text-gray-300">{it.rel}</span>, downscaled for person detection and scored at native resolution around the eyes.</p>
          )}
        </div>
        <div className="space-y-2 min-w-0">
          <h3 className="text-xs font-semibold text-gray-300">Output</h3>
          {isVlm && it.usage && (
            <div className="grid grid-cols-3 gap-2 text-xs tabular-nums">
              <Stat k="prompt tokens" v={fmtNum(it.usage.in)} />
              <Stat k="output tokens" v={fmtNum(it.usage.out)} />
              <Stat k="decode tok/s" v={it.usage.tok_s ?? '–'} />
              <Stat k="prefill" v={fmtDur(it.usage.prefill_s)} />
              <Stat k="decode" v={fmtDur(it.usage.decode_s)} />
              <Stat k="request total" v={fmtDur(it.usage.seconds)} />
            </div>
          )}
          {isVlm
            ? img?.vlm ? <Pre>{JSON.stringify(img.vlm, null, 2)}</Pre> : img && <p className="text-xs text-gray-500">No vision result stored.</p>
            : img?.local ? (
              <>
                <p className="text-xs text-gray-400"><TierBadge tier={img.local.local_tier} small /> {img.local.local_reason}</p>
                <Collapse label="Local stage result"><Pre>{JSON.stringify(img.local, null, 2)}</Pre></Collapse>
              </>
            ) : img && <p className="text-xs text-gray-500">No local result stored.</p>}
        </div>
      </div>
    </div>
  );
}

function Stat({ k, v }: { k: string; v: ReactNode }) {
  return <div className="rounded bg-gray-900 border border-gray-800 px-2 py-1"><div className="text-[10px] text-gray-500">{k}</div><div>{v}</div></div>;
}

function Pre({ label, children }: { label?: string; children: ReactNode }) {
  return (
    <div>
      {label && <div className="text-[10px] text-gray-500 mb-0.5">{label}</div>}
      <pre className="text-[11px] leading-snug text-gray-300 whitespace-pre-wrap break-words rounded bg-gray-900 border border-gray-800 p-2 max-h-80 overflow-auto">{children}</pre>
    </div>
  );
}

function Collapse({ label, children }: { label: string; children: ReactNode }) {
  return (
    <details className="text-xs">
      <summary className="cursor-pointer text-gray-400 hover:text-gray-200 select-none">{label}</summary>
      <div className="mt-1">{children}</div>
    </details>
  );
}

// ---- inputs / settings ----------------------------------------------------------------

function Inputs({ job }: { job: JobDetailT }) {
  const opts = Object.entries(job.options).filter(([, v]) => v !== null && v !== undefined);
  return (
    <Section title="Job inputs" tip="What the job was asked to do, and the runner settings it ran with.">
      <div className="grid md:grid-cols-3 gap-4 text-xs">
        <div className="min-w-0">
          <h3 className="text-gray-500 mb-1">Paths ({job.paths.length})</h3>
          <ul className="space-y-0.5 max-h-48 overflow-auto text-gray-300">
            {job.paths.map((p) => <li key={p} className="truncate" title={p}>{p}</li>)}
          </ul>
        </div>
        <div>
          <h3 className="text-gray-500 mb-1">Options</h3>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
            {opts.map(([k, v]) => <KV key={k} k={k} v={String(v)} />)}
          </dl>
        </div>
        <div>
          <h3 className="text-gray-500 mb-1">Timeline & runner</h3>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
            <KV k="created" v={fmtTime(job.created)} />
            <KV k="started" v={fmtTime(job.started)} />
            <KV k="finished" v={fmtTime(job.finished)} />
            {job.stages.vlm?.base_url && <KV k="model server" v={job.stages.vlm.base_url} />}
            <KV k="device" v={job.stages.local?.device ?? job.runner.device ?? '–'} />
            <KV k="default backend" v={`${job.runner.backend}${job.runner.model ? `/${job.runner.model}` : ''}`} />
          </dl>
        </div>
      </div>
      {job.message && <p className="mt-3 text-xs text-gray-400">{job.message}</p>}
    </Section>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  return <><dt className="text-gray-500">{k}</dt><dd className="text-gray-300 truncate" title={v}>{v}</dd></>;
}
