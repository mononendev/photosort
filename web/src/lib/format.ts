/** Number, time and error formatting shared across pages. */

type N = number | null | undefined;

/** A focus metric: 3 decimals, 4 below 0.01 (the eye thresholds live down there). */
export const fmt = (v: N) => (v === null || v === undefined ? '–' : v < 0.01 ? v.toFixed(4) : v.toFixed(3));

/** A duration in seconds, precise for short spans ("4.2s", "3m 10s", "1h 5m"). */
export function fmtDur(s: N): string {
  if (s === null || s === undefined || !isFinite(s)) return '–';
  if (s < 10) return `${s.toFixed(1)}s`;
  if (s < 90) return `${Math.round(s)}s`;
  if (s < 5400) return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
  return `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m`;
}

/** A remaining time at a glance ("40s", "12m", "1.5h"); empty when unknown. */
export function fmtEta(s: N): string {
  if (!s) return '';
  if (s < 90) return `${s}s`;
  if (s < 5400) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

export const fmtNum = (n: N) => (n === null || n === undefined ? '–' : n.toLocaleString());
export const fmtK = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}k` : String(n));
/** Unix seconds as a local date and time. */
export const fmtTime = (t: N) => (t ? new Date(t * 1000).toLocaleString() : '–');
export const fmtClock = (t: number) => new Date(t * 1000).toLocaleTimeString();
/** When something `s` seconds from now lands, in the device's timezone ("3:42 PM", "Sat 3:42 PM" past today). */
export function fmtFinishAt(s: N): string {
  if (s === null || s === undefined || !isFinite(s)) return '–';
  const at = new Date(Date.now() + s * 1000);
  const time = at.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  if (at.toDateString() === new Date().toDateString()) return time;
  return `${at.toLocaleDateString([], { weekday: 'short', ...(s > 6 * 86400 ? { month: 'short', day: 'numeric' } : {}) })} ${time}`;
}

/** done/total as a whole percentage, 0 when there is no total. */
export const pct = (done: number, total: number) => (total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0);

/** The message of whatever a query or mutation threw. */
export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));
