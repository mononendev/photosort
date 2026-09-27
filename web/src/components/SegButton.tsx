import type { MouseEvent, ReactNode } from 'react';

/** One option of a small segmented control or filter toggle. */
export default function SegButton({ on, onClick, children, className = 'text-xs px-2 py-0.5', title }: {
  on: boolean; onClick: (e: MouseEvent) => void; title?: string; children: ReactNode; className?: string;
}) {
  return (
    <button onClick={onClick} aria-pressed={on} title={title}
      className={`rounded ${className} ${on ? 'bg-gray-700 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-100'}`}>{children}</button>
  );
}
