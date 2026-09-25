import { useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';

/**
 * Hover/focus tooltip with rich content. The popover is portaled to <body> with fixed positioning so the
 * scrolling image modal can't clip it. `plain` drops the dotted underline for things that already look
 * interactive (badges, buttons).
 */
export default function Tip({ tip, children, plain, className = '' }: { tip: ReactNode; children: ReactNode; plain?: boolean; className?: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [pos, setPos] = useState<{ x: number; y: number; above: boolean } | null>(null);
  if (!tip) return <>{children}</>;
  const show = () => {
    const r = ref.current?.getBoundingClientRect();
    if (!r) return;
    const above = r.bottom > window.innerHeight * 0.6;
    setPos({ x: Math.min(Math.max(8, r.left), window.innerWidth - 368), y: above ? r.top - 6 : r.bottom + 6, above });
  };
  return (
    <span ref={ref} onMouseEnter={show} onMouseLeave={() => setPos(null)} onFocus={show} onBlur={() => setPos(null)} tabIndex={plain ? undefined : 0}
      className={`${plain ? '' : 'underline decoration-dotted decoration-gray-500 underline-offset-2 cursor-help'} ${className}`}>
      {children}
      {pos && createPortal(
        <div role="tooltip" style={{ left: pos.x, top: pos.y, transform: pos.above ? 'translateY(-100%)' : undefined }}
          className="fixed z-[100] w-[360px] rounded-lg border border-gray-700 bg-gray-900 px-3 py-2 text-xs leading-relaxed text-gray-200 shadow-2xl pointer-events-none font-sans normal-case tracking-normal whitespace-normal text-left">
          {tip}
        </div>,
        document.body,
      )}
    </span>
  );
}
