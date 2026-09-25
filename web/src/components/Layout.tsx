import { NavLink, Outlet } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import useStore from '../hooks/useStore';

const NAV = [
  { label: 'Dashboard', to: '/', end: true },
  { label: 'Browse', to: '/browse' },
  { label: 'Photos', to: '/photos' },
  { label: 'Jobs', to: '/jobs' },
  { label: 'Calibrate', to: '/calibrate' },
  { label: 'Export', to: '/export' },
];

export default function Layout() {
  const selected = useStore((s) => s.selected);
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: api.health, refetchInterval: 10000 });
  const { data: jobs } = useQuery({ queryKey: ['jobs'], queryFn: api.jobs, refetchInterval: 3000 });
  const active = jobs?.find((j) => j.state === 'running' || j.state === 'cancelling');
  const queued = jobs?.filter((j) => j.state === 'queued').length ?? 0;

  return (
    <div className="min-h-screen flex flex-col bg-gray-950 text-gray-100">
      <header className="border-b border-gray-800 bg-gray-900/80 backdrop-blur sticky top-0 z-40">
        <div className="max-w-7xl mx-auto px-4 h-14 flex items-center gap-6">
          <span className="font-semibold tracking-tight text-blue-400">photosort</span>
          <nav className="flex gap-1">
            {NAV.map((n) => (
              <NavLink
                key={n.to}
                to={n.to}
                end={n.end}
                className={({ isActive }) =>
                  `px-3 py-1.5 rounded-md text-sm ${isActive ? 'bg-gray-800 text-white' : 'text-gray-400 hover:text-gray-100 hover:bg-gray-800/60'}`
                }
              >
                {n.label}
                {n.to === '/browse' && selected.length > 0 && (
                  <span className="ml-1.5 text-xs bg-blue-600 text-white rounded-full px-1.5">{selected.length}</span>
                )}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-3 text-xs text-gray-400">
            {active && (
              <NavLink to="/jobs" className="flex items-center gap-2">
                <span className="inline-block w-2 h-2 rounded-full bg-blue-400 animate-pulse" />
                job #{active.id} {active.stage} {active.done}/{active.total}
                {queued > 0 && <span className="text-gray-500">+{queued} queued</span>}
              </NavLink>
            )}
            {health && (
              <span title={`${health.photos_root} · ${health.workdir}`}>
                {health.device ?? 'gpu: idle'} · {health.backend}
              </span>
            )}
          </div>
        </div>
      </header>
      <main className="flex-1 max-w-7xl w-full mx-auto px-4 py-6">
        <Outlet />
      </main>
      <footer className="text-center text-xs text-gray-600 py-4">
        photosort {__APP_VERSION__} · {__GIT_SHA__.slice(0, 7)}
      </footer>
    </div>
  );
}
