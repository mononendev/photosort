import { useEffect, useRef } from 'react';

type Handler = (e: KeyboardEvent) => void;

/**
 * Window-level keyboard shortcuts keyed by `KeyboardEvent.key` (letters matched case-insensitively). Ignored while
 * typing in a field or with Cmd/Ctrl/Alt held. `capture` listens in the capture phase and stops the event, so an
 * overlay (the fullscreen viewer) can take keys like Escape before the view under it sees them.
 * The handlers can change every render; the listener is bound once.
 */
export default function useHotkeys(keys: Record<string, Handler>, { capture = false } = {}) {
  const ref = useRef(keys);
  useEffect(() => { ref.current = keys; });
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (e.metaKey || e.ctrlKey || e.altKey || t?.closest?.('input, textarea, select, [contenteditable]')) return;
      const h = ref.current[e.key] ?? ref.current[e.key.toLowerCase()];
      if (!h) return;
      e.preventDefault();
      if (capture) e.stopPropagation();
      h(e);
    };
    window.addEventListener('keydown', onKey, capture);
    return () => window.removeEventListener('keydown', onKey, capture);
  }, [capture]);
}
