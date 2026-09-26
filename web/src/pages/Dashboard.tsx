import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api, TIERS } from '../api/client';
import JobRow from '../components/JobRow';
import { useBusy, useJobs } from '../hooks/useJobs';
import StatTile from '../components/StatTile';

export default function Dashboard() {
  const busy = useBusy();
  const { data: stats } = useQuery({ queryKey: ['stats'], queryFn: api.stats, refetchInterval: busy ? 5000 : false });
  const { data: jobs } = useJobs();
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: api.health });
  const recent = jobs?.slice(0, 5) ?? [];
  const t = stats?.tiers;
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-8 gap-3">
        <StatTile label="Tracked" value={stats?.tracked ?? '–'} to="/photos" tip="Images registered by any job. Folders you haven't processed aren't counted." />
        <StatTile label="Analyzed" value={stats?.analyzed ?? '–'} to="/photos?status=analyzed" sub="local focus scoring" tip="Images with local results: people found, eye bands and sharpness measured, and a local tier." />
        <StatTile label="Tagged" value={stats?.tagged ?? '–'} to="/photos?status=tagged" sub="vision model" tip="Images the vision model has tagged (its tier, subject, keywords, remarks, score)." />
        <StatTile label="Sharp" value={t?.tier3 ?? '–'} to="/photos?tier=3" tip="Shown tier 3: your call, else the configured focus source (the model by default)." />
        <StatTile label="Soft" value={t?.tier2 ?? '–'} to="/photos?tier=2" />
        <StatTile label="Partial" value={t?.tier1 ?? '–'} to="/photos?tier=1" />
        <StatTile label="Missed" value={t?.tier0 ?? '–'} to="/photos?tier=0" />
        <StatTile label="Needs review" value={stats?.review ?? '–'} to="/photos?review=1" sub="local ≠ model" tip="The local sharpness tier and the vision model's tier differ. Either the thresholds need calibrating or the model is being generous." />
      </div>

      <section>
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 mb-2">
          <h2 className="text-sm font-semibold text-gray-300">Recent jobs</h2>
          <Link to="/browse" className="text-sm text-blue-400 hover:underline">Pick folders to process →</Link>
        </div>
        {recent.length === 0 ? (
          <p className="text-sm text-gray-500">No jobs yet. Go to Browse, select folders or files, and start processing.</p>
        ) : (
          <div className="space-y-2">{recent.map((j) => <JobRow key={j.id} job={j} compact />)}</div>
        )}
      </section>

      {stats?.lr_by_tier && stats.lr_by_tier.length > 0 && (
        <section>
          <h2 className="text-sm font-semibold text-gray-300 mb-2">Your Lightroom ratings vs focus tier <span className="text-gray-500 font-normal">({stats.lr_rated} rated)</span></h2>
          <div className="overflow-x-auto"><table className="text-xs text-gray-300">
            <thead><tr><th className="text-left pr-4 text-gray-500">focus tier</th>{[0, 1, 2, 3, 4, 5].map((r) => <th key={r} className="px-2 text-gray-500">LR {r}★</th>)}</tr></thead>
            <tbody>{TIERS.map((t) => (
              <tr key={t}><td className="pr-4">{t}</td>{[0, 1, 2, 3, 4, 5].map((r) => <td key={r} className="px-2 text-center tabular-nums">{stats.lr_by_tier?.find((x) => x.tier === t && x.rating === r)?.n ?? ''}</td>)}</tr>
            ))}</tbody>
          </table></div>
        </section>
      )}
      {health && (
        <section className="text-xs text-gray-500 break-words">
          photos: <code>{health.photos_root}</code> · state: <code>{health.workdir}</code> · model server: <code>{health.ollama ?? 'n/a'}</code>
          {health.device ? <> · detector on <code>{health.device}</code></> : null}
        </section>
      )}
    </div>
  );
}
