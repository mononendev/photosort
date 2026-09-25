export default function Progress({ done, total, className = '' }: { done: number; total: number; className?: string }) {
  const pct = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0;
  return (
    <div className={`h-2 w-full rounded bg-gray-800 overflow-hidden ${className}`} title={`${done}/${total}`}>
      <div className="h-full bg-blue-500 transition-all" style={{ width: `${pct}%` }} />
    </div>
  );
}
