/** A centered, animated placeholder for a view whose data hasn't arrived yet: a turning aperture and what's loading. */
export default function Loading({ label = 'loading…', className = 'min-h-[60vh]' }: { label?: string; className?: string }) {
  return (
    <div role="status" aria-live="polite" className={`flex flex-col items-center justify-center gap-4 text-gray-400 animate-[fade-in_300ms_ease-out] ${className}`}>
      <svg viewBox="0 0 48 48" className="w-14 h-14 text-blue-400" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" aria-hidden>
        <circle cx="24" cy="24" r="21" className="opacity-25" />
        <g className="origin-center animate-[spin_2.4s_linear_infinite]">
          {/* six blades, each a chord offset from the centre, which leaves a hexagonal opening */}
          {[0, 60, 120, 180, 240, 300].map((a) => <line key={a} x1="24" y1="3" x2="36" y2="30" transform={`rotate(${a} 24 24)`} />)}
        </g>
        <circle cx="24" cy="24" r="21" strokeDasharray="22 110" className="origin-center animate-[spin_1.1s_linear_infinite]" />
      </svg>
      <span className="text-sm tracking-wide animate-pulse">{label}</span>
    </div>
  );
}
