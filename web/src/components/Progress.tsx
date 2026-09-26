import { pct } from '../lib/format';

export default function Progress({ done, total, className = '' }: { done: number; total: number; className?: string }) {
  return (
    <div className={`h-2 w-full rounded bg-gray-800 overflow-hidden ${className}`} title={`${done}/${total}`}>
      <div className="h-full bg-blue-500 transition-all" style={{ width: `${pct(done, total)}%` }} />
    </div>
  );
}
