import { useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api, isLive } from '../api/client';
import useStore from '../hooks/useStore';
import { useJobs, useRefreshOnJobEnd } from '../hooks/useJobs';

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
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: api.health, refetchInterval: 30000 });
  const { data: jobs } = useJobs();
  useRefreshOnJobEnd();
  const active = jobs?.find(isLive);
  const queued = jobs?.filter((j) => j.state === 'queued').length ?? 0;
  // The phone menu closes on navigation: remember which page it was opened on.
  const { pathname } = useLocation();
  const [menuAt, setMenuAt] = useState<string | null>(null);
  const menu = menuAt === pathname;

  const links = (mobile: boolean) => NAV.map((n) => (
    <NavLink
      key={n.to}
      to={n.to}
      end={n.end}
      className={({ isActive }) =>
        `rounded-md transition-colors active:bg-gray-700 ${mobile ? 'px-3 py-2.5 text-base' : 'px-3 py-1.5 text-sm'} ${isActive ? 'bg-gray-800 text-white' : 'text-gray-400 hover:text-gray-100 hover:bg-gray-800/60'}`
      }
    >
      {n.label}
      {n.to === '/browse' && selected.length > 0 && (
        <span className="ml-1.5 text-xs bg-blue-600 text-white rounded-full px-1.5">{selected.length}</span>
      )}
    </NavLink>
  ));
  const healthText = health && (
    <span title={`${health.photos_root} · ${health.workdir}`}>
      {health.device ?? 'gpu: idle'} · {health.backend}
    </span>
  );

  return (
    <div className="min-h-screen flex flex-col bg-gray-950 text-gray-100">
      <header className="border-b border-gray-800 bg-gray-900/80 backdrop-blur sticky top-0 z-40 pt-[env(safe-area-inset-top)]">
        <div className="max-w-7xl mx-auto px-3 sm:px-4 h-14 flex items-center gap-3 md:gap-6">
          <span className="font-semibold tracking-tight text-blue-400">photosort</span>
          <nav className="hidden md:flex gap-1">{links(false)}</nav>
          <div className="ml-auto flex items-center gap-3 text-xs text-gray-400 min-w-0">
            {active && (
              <NavLink to={`/jobs/${active.id}`} className="flex items-center gap-2 min-w-0">
                <span className="inline-block w-2 h-2 shrink-0 rounded-full bg-blue-400 animate-pulse" />
                <span className="truncate">
                  <span className="hidden sm:inline">job </span>#{active.id} <span className="hidden sm:inline">{active.stage} </span>{active.done}/{active.total}
                </span>
                {queued > 0 && <span className="hidden sm:inline text-gray-500">+{queued} queued</span>}
              </NavLink>
            )}
            <span className="hidden lg:inline">{healthText}</span>
          </div>
          <button onClick={() => setMenuAt(menu ? null : pathname)} aria-label="Menu" aria-expanded={menu}
            className="md:hidden -mr-1 p-2 rounded-md text-gray-300 active:bg-gray-800">
            <svg viewBox="0 0 24 24" className="w-6 h-6" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round">
              {menu ? <path d="M6 6l12 12M18 6L6 18" /> : <path d="M4 7h16M4 12h16M4 17h16" />}
            </svg>
          </button>
        </div>
        {menu && (
          <nav className="md:hidden border-t border-gray-800 px-3 py-2 grid grid-cols-2 gap-1 animate-[menu-in_120ms_ease-out]">
            {links(true)}
            {health && <div className="col-span-2 px-3 pt-2 text-xs text-gray-500">{healthText}</div>}
          </nav>
        )}
      </header>
      <main className="flex-1 max-w-7xl w-full mx-auto px-3 sm:px-4 py-4 sm:py-6">
        <Outlet />
      </main>
      <footer className="text-center text-xs text-gray-600 py-4 pb-[max(1rem,env(safe-area-inset-bottom))]">
        photosort {__APP_VERSION__} · {__GIT_SHA__.slice(0, 7)}
      </footer>
    </div>
  );
}
