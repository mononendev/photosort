import type { FocusDebug, FocusView, LocalResult, Person, SpectrumView } from '../api/client';
import Gauge from './Gauge';
import Tip from './Tip';
import { fmt } from './CheckTable';
import { METRIC_TIPS, explainLocal } from '../lib/explain';
import type { Cfg } from '../lib/explain';
import { GRADE_COLOR, PERSON_COLORS, gradePerson } from '../lib/pose';

/** Per-person numbers: why they rank where they do, and each metric against its tier thresholds. */
export function PersonInspector({ l, cfg, selected, onSelect }: { l: LocalResult; cfg: Cfg; selected: number; onSelect: (i: number) => void }) {
  const people = l.people ?? [];
  if (!people.length) return <div className="text-xs text-gray-500">No people found, so there is nothing to grade: local tier {l.local_tier} ({l.local_reason}).</div>;
  const i = Math.min(Math.max(selected, 0), people.length - 1);
  const p = people[i];
  const g = gradePerson(p, cfg);
  const others = [
    { label: 'head', value: p.sharp_head }, { label: 'torso', value: p.sharp_torso },
    { label: 'body', value: p.sharp_body }, { label: 'bg', value: l.bg_sharp },
  ];
  const thr = (cfg?.focus ?? {}) as Record<string, number>;
  return (
    <div className="rounded-lg border border-gray-800 p-3 space-y-3">
      <div className="flex flex-wrap items-center gap-1 text-xs">
        <span className="uppercase tracking-wide text-gray-500 mr-1">people</span>
        {people.map((q, j) => {
          const gj = gradePerson(q, cfg).grade;
          return (
            <button key={j} onClick={() => onSelect(j)} className={`px-2 py-0.5 rounded border ${j === i ? 'border-gray-400 bg-gray-800' : 'border-gray-700 hover:border-gray-500'}`}>
              <span style={{ color: PERSON_COLORS[j % PERSON_COLORS.length] }}>#{j + 1}</span>
              <span className="ml-1" style={{ color: GRADE_COLOR[gj ?? 'none'] }}>{gj ?? '–'}</span>
            </button>
          );
        })}
        <span className="ml-auto text-gray-500">{l.n_people} found</span>
      </div>

      <div className="grid sm:grid-cols-2 gap-4">
        <div className="space-y-2">
          <div className="text-xs text-gray-300">
            <span style={{ color: PERSON_COLORS[i % PERSON_COLORS.length] }}>Person #{i + 1}</span>
            {i === 0 ? ' · primary subject (decides the tier)' : ' · secondary (can only lift a missed primary to tier 1)'}
          </div>
          <Tip tip={<>Who counts as the subject: the highest priority wins. Big, central and confidently detected people rank first. Numbers are for this person.</>}>
            <div className="font-mono text-[11px] text-gray-400 leading-5">
              priority = area × (1 − ½·dist) × (½ + ½·conf)<br />
              <span className="text-gray-200">{fmt(p.priority)}</span> = {fmt(p.area_frac)} × (1 − ½·{fmt(p.center_dist)}) × (½ + ½·{fmt(p.conf)})
            </div>
          </Tip>
          <div className="text-[11px] text-gray-500">
            head box via {p.head_src} · eyes via {p.eye_src ?? 'nothing (not located)'}{p.face ? ` · face score ${fmt(p.face.score)}` : ''}
          </div>
          <div className="text-xs">
            grade <b style={{ color: GRADE_COLOR[g.grade ?? 'none'] }}>{g.grade ?? '–'}</b>
            <span className="text-gray-500"> — {g.onEyes ? 'every eye-band metric must clear a tier’s threshold' : 'no eye band, so the head box decides on its own thresholds'}</span>
          </div>
        </div>
        <div className="space-y-2">
          {g.checks.map((c) => (
            <Gauge key={c.label} label={c.label} value={c.value} t1={c.t1} t2={c.t2}
              tip={c.label.includes('FFT') ? METRIC_TIPS.fft : c.label.includes('eye') ? METRIC_TIPS.eyes : METRIC_TIPS.head} />
          ))}
          {g.onEyes && <Gauge label="head box Laplacian (not deciding)" value={p.sharp_head} t1={thr.tier1_min} t2={thr.tier2_min} tip={METRIC_TIPS.head} />}
          <Gauge label="regions compared" value={p.sharp_head} refs={others.slice(1)} note="(head)"
            tip={<>Head against torso, whole body and background on the same log axis. Torso or background well right of the head suggests focus landed behind or below the face.</>} />
        </div>
      </div>
      {i === 0 && <div className="text-xs text-gray-300 border-t border-gray-800 pt-2">{explainLocal(l, cfg)}</div>}
    </div>
  );
}

const px = (s: number[]) => `${s[0]}×${s[1]}`;

function LapFormula({ v }: { v: FocusView }) {
  return (
    <div className="font-mono text-[11px] text-gray-400">
      var(∇²I) / (var(I) + ε) = {v.lap_var.toExponential(2)} / ({v.gray_var.toExponential(2)} + {v.eps}) = <span className="text-gray-100">{fmt(v.value)}</span>
      <div className="text-gray-500">measured at {px(v.size)} after a σ=1 blur</div>
    </div>
  );
}

function Spectrum({ s }: { s: SpectrumView }) {
  const [w, h] = s.size;
  const rings = [{ f: s.floor, c: '#9ca3af', t: 'floor' }, { f: s.band[0], c: '#34d399', t: 'band' }, { f: s.band[1], c: '#34d399', t: '' }, { f: 1, c: '#6b7280', t: 'Nyquist' }];
  return (
    <div className="relative">
      <img src={s.img} alt="spectrum" className="h-28 w-auto rounded" style={{ aspectRatio: `${w} / ${h}` }} />
      <svg viewBox={`0 0 ${w} ${h}`} className="absolute inset-0 h-28 w-auto" style={{ aspectRatio: `${w} / ${h}` }}>
        {rings.map((r) => <ellipse key={r.f} cx={w / 2} cy={h / 2} rx={(r.f * w) / 2} ry={(r.f * h) / 2} fill="none" stroke={r.c} strokeWidth={1} vectorEffect="non-scaling-stroke" strokeDasharray={r.t === 'floor' || r.t === 'Nyquist' ? '3 3' : undefined} />)}
      </svg>
    </div>
  );
}

/** Radial energy of the eye band's spectrum; the FFT ratio is the green area over the green + blue area. */
function Profile({ s }: { s: SpectrumView }) {
  const { edges, energy } = s.profile;
  const W = 320, H = 110, pad = 18, right = 10;
  const vals = energy.map((e) => Math.log10(Math.max(e, 1e-7)));
  const lo = Math.min(...vals), hi = Math.max(...vals);
  const x = (f: number) => pad + f * (W - pad - right);
  const y = (v: number) => H - pad - ((v - lo) / (hi - lo || 1)) * (H - pad - 4);
  const fill = (a: number) => (a < s.floor || a >= s.band[1] ? '#374151' : a >= s.band[0] ? '#34d399' : '#60a5fa');
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full max-w-[420px]">
      {energy.map((_, k) => (
        <rect key={k} x={x(edges[k])} y={y(vals[k])} width={Math.max(0.5, x(edges[k + 1]) - x(edges[k]) - 0.5)} height={H - pad - y(vals[k])} fill={fill(edges[k])}>
          <title>{`${edges[k].toFixed(2)}–${edges[k + 1].toFixed(2)} × Nyquist: ${(energy[k] * 100).toFixed(2)}% of counted energy`}</title>
        </rect>
      ))}
      {[0, 0.25, 0.5, 0.75, 1].map((f) => <text key={f} x={x(f)} y={H - 4} fontSize={9} fill="#9ca3af" textAnchor="middle">{f}</text>)}
      <text x={W - 2} y={10} fontSize={9} fill="#9ca3af" textAnchor="end">log energy by frequency (× Nyquist)</text>
    </svg>
  );
}

/** The eye band and head box as the metrics see them: pixels, Laplacian response, spectrum. */
export function FocusMath({ d, p, loading, error }: { d?: FocusDebug['people'][number]; p?: Person; loading: boolean; error?: string }) {
  if (loading) return <div className="text-xs text-gray-500">Reading the original file and recomputing…</div>;
  if (error) return <div className="text-xs text-red-300">{error}</div>;
  if (!d || !p) return <div className="text-xs text-gray-500">Nothing measured for this person.</div>;
  return (
    <div className="space-y-4">
      {d.eye ? (
        <div className="space-y-2">
          <div className="text-xs text-gray-300">Eye band <span className="text-gray-500">({p.eye_src === 'face' ? 'face landmarks' : 'pose keypoints'}, native pixels)</span></div>
          <div className="flex flex-wrap gap-3 items-start">
            <figure><img src={d.eye.img} alt="eye band" className="h-28 w-auto rounded" style={{ imageRendering: 'pixelated' }} /><figcaption className="text-[10px] text-gray-500">pixels</figcaption></figure>
            {d.eye.laplacian && <figure><img src={d.eye.laplacian.img} alt="laplacian" className="h-28 w-auto rounded" style={{ imageRendering: 'pixelated' }} /><figcaption className="text-[10px] text-gray-500">|Laplacian|: bright = edges</figcaption></figure>}
            {d.eye.spectrum && <figure><Spectrum s={d.eye.spectrum} /><figcaption className="text-[10px] text-gray-500">power spectrum; rings at floor, band, Nyquist</figcaption></figure>}
          </div>
          {d.eye.laplacian && <LapFormula v={d.eye.laplacian} />}
          {d.eye.spectrum && (
            <div className="space-y-1">
              <Profile s={d.eye.spectrum} />
              <div className="font-mono text-[11px] text-gray-400">
                E[{d.eye.spectrum.band[0]}–{d.eye.spectrum.band[1]}] / E[{d.eye.spectrum.floor}–{d.eye.spectrum.band[1]}] = {d.eye.spectrum.band_energy.toExponential(2)} / {d.eye.spectrum.total_energy.toExponential(2)} = <span className="text-gray-100">{fmt(d.eye.spectrum.value)}</span>
                <div className="text-gray-500"><span className="text-emerald-400">green</span> counts toward the ratio, <span className="text-blue-400">blue</span> only toward the total, grey is ignored (DC/gradients below, noise and JPEG ringing above). Defocus drains the green first.</div>
              </div>
            </div>
          )}
        </div>
      ) : <div className="text-xs text-gray-500">No eye band for this person{p.eye_src ? '' : ' (eyes not located)'}.</div>}
      {d.head && (
        <div className="space-y-2">
          <div className="text-xs text-gray-300">Head box <span className="text-gray-500">(via {p.head_src})</span></div>
          <div className="flex flex-wrap gap-3">
            <figure><img src={d.head.img} alt="head" className="h-28 w-auto rounded" /><figcaption className="text-[10px] text-gray-500">pixels</figcaption></figure>
            {d.head.laplacian && <figure><img src={d.head.laplacian.img} alt="head laplacian" className="h-28 w-auto rounded" /><figcaption className="text-[10px] text-gray-500">|Laplacian|</figcaption></figure>}
          </div>
          {d.head.laplacian && <LapFormula v={d.head.laplacian} />}
        </div>
      )}
    </div>
  );
}
