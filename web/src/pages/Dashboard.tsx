import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import JobRow from '../components/JobRow';

function Tile({ label, value, to, sub }: { label: string; value: number | string; to?: string; sub?: string }) {
  const body = (
    <div className="rounded-lg border border-gray-800 bg-gray-900 p-4 hover:border-gray-700">
      <div className="text-xs uppercase tracking-wide text-gray-500">{label}</div>
      <div className="text-2xl font-semibold mt-1 tabular-nums">{value}</div>
      {sub && <div className="text-xs text-gray-500 mt-1">{sub}</div>}
    </div>
  );
  return to ? <Link to={to}>{body}</Link> : body;
}

export default function Dashboard() {
  const { data: stats } = useQuery({ queryKey: ['stats'], queryFn: api.stats, refetchInterval: 5000 });
  const { data: jobs } = useQuery({ queryKey: ['jobs'], queryFn: api.jobs, refetchInterval: 3000 });
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: api.health });
  const recent = jobs?.slice(0, 5) ?? [];
  const t = stats?.tiers;
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-7 gap-3">
        <Tile label="Tracked" value={stats?.tracked ?? '–'} to="/photos" />
        <Tile label="Analyzed" value={stats?.analyzed ?? '–'} to="/photos?status=analyzed" sub="local focus scoring" />
        <Tile label="Tagged" value={stats?.tagged ?? '–'} to="/photos?status=tagged" sub="vision model" />
        <Tile label="Sharp" value={t?.tier2 ?? '–'} to="/photos?tier=2" />
        <Tile label="Partial" value={t?.tier1 ?? '–'} to="/photos?tier=1" />
        <Tile label="Nobody in focus" value={t?.tier0 ?? '–'} to="/photos?tier=0" />
        <Tile label="Needs review" value={stats?.review ?? '–'} to="/photos?review=1" sub="local ≠ model" />
      </div>

      <section>
        <div className="flex items-center justify-between mb-2">
          <h2 className="text-sm font-semibold text-gray-300">Recent jobs</h2>
          <Link to="/browse" className="text-sm text-blue-400 hover:underline">Pick folders to process →</Link>
        </div>
        {recent.length === 0 ? (
          <p className="text-sm text-gray-500">No jobs yet. Go to Browse, select folders or files, and start processing.</p>
        ) : (
          <div className="space-y-2">{recent.map((j) => <JobRow key={j.id} job={j} compact />)}</div>
        )}
      </section>

      {health && (
        <section className="text-xs text-gray-500">
          photos: <code>{health.photos_root}</code> · state: <code>{health.workdir}</code> · model server: <code>{health.ollama ?? 'n/a'}</code>
          {health.device ? <> · detector on <code>{health.device}</code></> : null}
        </section>
      )}
    </div>
  );
}
