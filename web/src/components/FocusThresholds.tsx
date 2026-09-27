import { Fragment, useState } from 'react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api, cropUrl, FOCUS_METRIC_LABEL, TIER_COLOR, TIER_LABEL } from '../api/client';
import type { Calibration, FocusMetric, RescoreResult } from '../api/client';
import { errMsg } from '../lib/format';
import SegButton from './SegButton';
import Tip from './Tip';
import { AppliesChip, StricterChip } from './TuningPanel';

const METRICS: { m: FocusMetric; prefix: string; when: string; tip: string }[] = [
  { m: 'eye', prefix: 'eye_', when: 'eyes located',
    tip: 'Contrast-normalized Laplacian on the band across both eyes: how much crisp fine edge there is. Decides the tier (together with the FFT ratio) whenever the eyes were found.' },
  { m: 'hf', prefix: 'hf_', when: 'eyes located, with the Laplacian',
    tip: 'Share of the eye band’s detail in the upper-mid frequencies. It falls off faster than the Laplacian on slight softness, so it mostly separates 3 from 2. A photo gets the lower of the two eye-band tiers. Skipped when “require the FFT ratio too” is off.' },
  { m: 'head', prefix: '', when: 'no eyes (helmet, visor, turned away)',
    tip: 'Laplacian on the whole head box. Decides on its own when no eyes were located, and must clear the tier as well when the eye band looks like sunglasses or goggles.' },
];
const TIER_KEYS = [1, 2, 3] as const;
const key = (prefix: string, t: number) => `${prefix}tier${t}_min`;
const sig = (v: number) => Number(v.toPrecision(3));

/** Share of photos (0-100) scoring at or above v, read off the 101 quantiles. */
function reach(q: number[] | undefined, v: number): number | null {
  if (!q?.length || !Number.isFinite(v)) return null;
  if (v <= q[0]) return 100;
  if (v > q[100]) return 0;
  const i = q.findIndex((x) => x >= v);
  const lo = q[i - 1], hi = q[i];
  const rank = i - 1 + (hi > lo ? (v - lo) / (hi - lo) : 1);
  return Math.max(0, Math.min(100, 100 - rank));
}

const tierOf = (v: number, cuts: number[]) => cuts.reduce((t, c, i) => (v >= c ? i + 1 : t), 0);   // cuts = [t1, t2, t3]

/** Tier shares on this metric alone, as a bar: each tier's width is the share of photos that land in it. */
function Ladder({ q, cuts }: { q?: number[]; cuts: number[] }) {
  const r = cuts.map((c) => reach(q, c));
  if (!q || r.some((x) => x == null)) return null;
  const at = [100, ...(r as number[]), 0];   // share reaching tier 0, 1, 2, 3, (none)
  const shares = [0, 1, 2, 3].map((t) => Math.max(0, at[t] - at[t + 1]));
  return (
    <Tip plain tip="Share of analyzed photos that land in each tier on this metric alone, with the cuts as typed. The real tier also depends on the other metric and the rules below, so treat it as a guide.">
      <div className="flex h-5 w-full overflow-hidden rounded text-[10px] font-medium text-gray-950 cursor-help">
        {shares.map((s, t) => s > 0 && (
          <div key={t} style={{ width: `${s}%`, background: TIER_COLOR[t] }} className="flex items-center justify-center overflow-hidden whitespace-nowrap">
            {s >= 9 ? `${t} · ${Math.round(s)}%` : s >= 4 ? `${Math.round(s)}%` : ''}
          </div>
        ))}
      </div>
    </Tip>
  );
}

/** The nine tier cuts (three metrics × three tiers) with live pass rates, plus the crops to find them by eye. */
export default function FocusThresholds({ onSave, saving, saved, error }: {
  onSave: (values: Record<string, number>) => void; saving: boolean; saved?: RescoreResult; error?: unknown;
}) {
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config });
  const { data: defs } = useQuery({ queryKey: ['config-defaults'], queryFn: api.configDefaults, staleTime: Infinity });
  const cals = useQueries({ queries: METRICS.map(({ m }) => ({ queryKey: ['calibration', m], queryFn: () => api.calibration(48, m) })) });
  const cal: Partial<Record<FocusMetric, Calibration>> = Object.fromEntries(METRICS.map(({ m }, i) => [m, cals[i].data]));
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [metric, setMetric] = useState<FocusMetric>('eye');

  const focus = (cfg?.focus ?? {}) as Record<string, number>;
  const dfocus = (defs?.focus ?? {}) as Record<string, number>;
  const text = (k: string) => edits[k] ?? (focus[k] != null ? String(focus[k]) : '');
  const num = (k: string) => Number(text(k));
  const setVal = (k: string, v: number | string) => setEdits((e) => ({ ...e, [k]: String(v) }));
  const all = METRICS.flatMap(({ prefix }) => TIER_KEYS.map((t) => key(prefix, t)));
  const changed = all.filter((k) => edits[k] !== undefined && num(k) !== focus[k]);
  const badValue = all.some((k) => !(num(k) > 0));
  const disorder = METRICS.filter(({ prefix }) => !(num(key(prefix, 1)) < num(key(prefix, 2)) && num(key(prefix, 2)) < num(key(prefix, 3))));

  const sel = METRICS.find((x) => x.m === metric)!;
  const selCuts = TIER_KEYS.map((t) => num(key(sel.prefix, t)));
  const samples = cal[metric]?.samples ?? [];

  return (
    <section className="space-y-3">
      <h2 className="font-semibold">Local focus thresholds</h2>
      <div className="rounded-lg border border-gray-800 bg-gray-900/40 p-3 text-sm text-gray-300 space-y-2 max-w-4xl">
        <p><b>Higher is sharper.</b> Each metric scores how much crisp fine detail a region holds. A cut is the <em>minimum</em> score a photo needs to reach that tier: <b>raise a cut to be stricter</b> (fewer photos reach it), lower it to be more lenient. Within a metric the cuts must climb from tier 1 to tier 3.</p>
        <p className="text-gray-400">Which cuts apply: if the eyes were located, the eye band must clear <em>both</em> the Laplacian and the FFT cut for a tier, so the lower of the two decides. With no eyes, the head box Laplacian decides alone. Sunglasses or goggles (see eyewear ratio below) make the head box count too. The rules in the next section can then take a tier 3 down to 2.</p>
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-400">
          {[0, 1, 2, 3].map((t) => <span key={t}><span className="inline-block w-2.5 h-2.5 rounded-sm mr-1 align-middle" style={{ background: TIER_COLOR[t] }} />{t} {TIER_LABEL[t]}</span>)}
          <StricterChip dir="higher" /><AppliesChip a="rescore" />
        </div>
      </div>

      <div className="space-y-3">
        {METRICS.map(({ m, prefix, when, tip }) => {
          const q = cal[m]?.quantiles;
          const cuts = TIER_KEYS.map((t) => num(key(prefix, t)));
          return (
            <div key={m} className={`rounded-lg border p-3 ${disorder.some((d) => d.m === m) ? 'border-red-700' : 'border-gray-800'} bg-gray-900`}>
              <div className="flex flex-wrap items-baseline gap-x-3 mb-2">
                <Tip tip={tip}><span className="font-medium">{FOCUS_METRIC_LABEL[m]}</span></Tip>
                <span className="text-xs text-gray-500">decides when: {when}</span>
                {cal[m]?.count != null && <span className="text-xs text-gray-600">{cal[m]!.count} photos measured</span>}
              </div>
              <div className="grid gap-3 sm:grid-cols-3">
                {TIER_KEYS.map((t) => {
                  const k = key(prefix, t), v = num(k), r = reach(q, v), def = dfocus[k];
                  const isChanged = edits[k] !== undefined && v !== focus[k];
                  return (
                    <div key={t} className="space-y-1">
                      <div className="text-xs" style={{ color: TIER_COLOR[t] }}>tier {t} · {TIER_LABEL[t]} needs ≥</div>
                      <div className="flex items-center gap-1.5 text-sm">
                        <Tip plain tip="More lenient: lower this cut 5%, so more photos reach the tier.">
                          <button onClick={() => setVal(k, sig(v * 0.95))} className="px-1.5 rounded bg-gray-800 hover:bg-gray-700 text-gray-300" aria-label="more lenient">−</button>
                        </Tip>
                        <input value={text(k)} onChange={(e) => setVal(k, e.target.value)} inputMode="decimal"
                          className={`bg-gray-950 border rounded px-2 py-1 w-20 tabular-nums text-sm ${!(v > 0) ? 'border-red-500' : isChanged ? 'border-blue-500' : 'border-gray-700'}`} />
                        <Tip plain tip="Stricter: raise this cut 5%, so fewer photos reach the tier.">
                          <button onClick={() => setVal(k, sig(v * 1.05))} className="px-1.5 rounded bg-gray-800 hover:bg-gray-700 text-gray-300" aria-label="stricter">+</button>
                        </Tip>
                      </div>
                      <div className="text-xs text-gray-500 flex flex-wrap gap-x-2">
                        {r != null && <Tip tip={`Of all measured photos, ${r.toFixed(1)}% score at least this on this metric.`}><span className={isChanged ? 'text-blue-300' : ''}>{Math.round(r)}% reach</span></Tip>}
                        {def != null && def !== v && <button onClick={() => setVal(k, def)} className="hover:text-gray-200">default {def} ↺</button>}
                      </div>
                    </div>
                  );
                })}
              </div>
              <div className="mt-2"><Ladder q={q} cuts={cuts} /></div>
            </div>
          );
        })}
      </div>

      <div className="flex flex-wrap items-center gap-3 text-sm">
        <button onClick={() => onSave(Object.fromEntries(changed.map((k) => [k, num(k)])))} disabled={!changed.length || badValue || disorder.length > 0 || saving}
          className="px-3 py-1 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-40">save {changed.length || ''} + re-score</button>
        {changed.length > 0 && <button onClick={() => setEdits({})} className="text-gray-400 hover:text-gray-100">discard</button>}
        <button onClick={() => setEdits(Object.fromEntries(all.filter((k) => dfocus[k] != null).map((k) => [k, String(dfocus[k])])))} className="text-gray-500 hover:text-gray-200">all defaults</button>
        {disorder.length > 0 && <span className="text-red-400">{disorder.map((d) => FOCUS_METRIC_LABEL[d.m]).join(', ')}: tier 1 &lt; tier 2 &lt; tier 3 needed</span>}
        {saving && <span className="text-gray-500">re-scoring…</span>}
        {saved && !changed.length && <span className="text-gray-400">{saved.changed} photos changed tier · AF points read on {saved.af_backfilled} · primary re-picked on {saved.primary_changed}{saved.errors > 0 && <span className="text-amber-400" title={saved.first_error ?? ''}> · {saved.errors} failed</span>}</span>}
        {error != null && <span className="text-red-400">re-score failed: {errMsg(error)}</span>}
      </div>

      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-gray-500">find the cuts by eye:</span>
          {METRICS.map(({ m, tip }) => <Tip key={m} plain tip={tip}><SegButton on={m === metric} onClick={() => setMetric(m)} className="px-3 py-1">{FOCUS_METRIC_LABEL[m]}</SegButton></Tip>)}
        </div>
        <p className="text-xs text-gray-500 max-w-3xl">The primary subject's crop from 48 photos spread evenly from softest to sharpest on this metric. The dashed markers are the cuts as typed above and move as you edit. Put each one where the crops change from miss to soft, soft to slightly soft, and slightly soft to sharp. The colored number is the tier on this metric alone; “now” is the photo's current local tier.</p>
        <div className="grid gap-2 grid-cols-[repeat(auto-fill,minmax(110px,1fr))] sm:grid-cols-[repeat(auto-fill,minmax(150px,1fr))]">
          {samples.map((s, i) => {
            const t = tierOf(s.sharp, selCuts);
            const prev = i ? tierOf(samples[i - 1].sharp, selCuts) : 0;
            return (
              <Fragment key={s.id}>
                {TIER_KEYS.filter((c) => c > prev && c <= t).map((c) => (
                  <div key={`cut${c}`} className="rounded-lg border-2 border-dashed flex flex-col items-center justify-center text-xs p-2 min-h-24" style={{ borderColor: TIER_COLOR[c], color: TIER_COLOR[c] }}>
                    <span>tier {c} from here</span><span className="font-mono">≥ {selCuts[c - 1]}</span>
                  </div>
                ))}
                <div className="rounded-lg overflow-hidden border border-gray-800 bg-gray-900">
                  <img src={cropUrl(s.id)} alt="" loading="lazy" className="w-full aspect-square object-cover" />
                  <div className="px-2 py-1 text-xs font-mono flex justify-between gap-1">
                    <span>{s.sharp.toFixed(4)}</span>
                    <span><span style={{ color: TIER_COLOR[t] }}>{t}</span> <span className="text-gray-500">now {s.tier}</span></span>
                  </div>
                </div>
              </Fragment>
            );
          })}
        </div>
      </div>
    </section>
  );
}
