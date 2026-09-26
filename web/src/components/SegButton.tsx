import type { ReactNode } from 'react';

/** One option of a small segmented control or filter toggle. */
export default function SegButton({ on, onClick, children, className = 'text-xs px-2 py-0.5' }: {
  on: boolean; onClick: () => void; children: ReactNode; className?: string;
}) {
  return (
    <button onClick={onClick} aria-pressed={on}
      className={`rounded ${className} ${on ? 'bg-gray-700 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-100'}`}>{children}</button>
  );
}
