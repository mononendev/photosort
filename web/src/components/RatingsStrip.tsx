import type { ReactNode } from 'react';
import { RATINGS, TIER_CLASS, TIER_LABEL } from '../api/client';
import type { ImageDetail } from '../api/client';
import { TIER_MEANING, explainDisagree, explainFinal, explainSplit, splitShort } from '../lib/explain';
import type { Cfg } from '../lib/explain';
import { Stars } from './TierBadge';
import Tip from './Tip';

const chip = 'rounded border px-1.5 py-0.5 text-[11px] leading-none';
const none = `${chip} border-gray-700 bg-gray-800 text-gray-500`;

function Tier({ t }: { t: number | null | undefined }) {
  return t == null ? <span className={none}>–</span> : <span className={`${chip} ${TIER_CLASS[t]}`}>{t} {TIER_LABEL[t]}</span>;
}

function Item({ k, tip, children }: { k: string; tip: ReactNode; children: ReactNode }) {
  return (
    <Tip plain tip={tip} className="inline-flex items-center gap-1 cursor-help">
      <span className="text-[10px] uppercase tracking-wide text-gray-500">{k}</span>{children}
    </Tip>
  );
}

/**
 * Every verdict on one photo side by side (for the zoomed viewer): the final tier, your rating, the vision model,
 * the local tier, quality/keeper, imported truth and the Lightroom sidecar. Hover any of them for the reasoning.
 */
export default function RatingsStrip({ d, cfg, localTip }: { d: ImageDetail; cfg: Cfg; localTip: ReactNode }) {
  const v = d.vlm, l = d.local, o = d.override;
  const r = d.reviewed && d.rating != null ? RATINGS[d.rating] : undefined;
  const yourScore = o?.quality_score != null || o?.keeper != null;
  const disagree = !d.vlm_stale && l && d.focus_tier_local != null && d.focus_tier_vlm != null && d.focus_tier_local !== d.focus_tier_vlm;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
      <Item k="final" tip={explainFinal(d, cfg)}><Tier t={d.focus_tier} /></Item>
      <Item k="you" tip={r
        ? <div className="space-y-1"><div><b>Your call: {r.label}</b>{r.value < 4 ? <> ({TIER_MEANING[r.value]})</> : <>: sharp, and one of the best.</>}</div>
          {o?.note && <div>Your note: “{o.note}”</div>}
          <div className="text-gray-400">Beats the local and model tiers everywhere, and counts as calibration truth. q w e r t to change, ⌫ to clear.</div></div>
        : <>You haven't rated this photo yet. q w e r t rate it (0 missed, 1 soft, 2 slightly soft, 3 sharp, ★ banger).{o?.note && <> Your note: “{o.note}”</>}</>}>
        {r ? <span className={`${chip} ${r.cls}`}>{r.short} {r.label}</span> : <span className={none}>unrated</span>}
      </Item>
      <Item k="model" tip={v
        ? <div className="space-y-1"><div><b>Model tier {v.focus_tier}</b>: {TIER_MEANING[v.focus_tier]}.</div>
          {v.focus_notes && <div>“{v.focus_notes}”</div>}
          {d.vlm_stale && <div className="text-amber-300">Stale: the model judged the frame before its exposure lift, so this doesn't count until it re-tags.</div>}</div>
        : d.status === 'skipped' ? <>Skipped by the vision model ({d.vlm_skip}).</> : <>Not yet tagged by the vision model.</>}>
        <Tier t={v?.focus_tier} />{d.vlm_stale && <span className="text-[10px] text-amber-300">stale</span>}
      </Item>
      <Item k="local" tip={localTip ?? 'The local stage (pose, face and sharpness metrics) hasn’t run on this photo yet.'}>
        <Tier t={l?.local_tier} />
      </Item>
      <Item k="score" tip={<div className="space-y-1">
        <div><b>{d.quality_score ?? '–'}/5 · {d.keeper ? 'keeper' : d.keeper === false ? 'cull' : 'no verdict'}</b> ({yourScore ? 'your call' : 'the model'})</div>
        {yourScore && v && <div className="text-gray-400">The model said {v.quality_score}/5, {v.keeper ? 'keeper' : 'cull'}.</div>}
        {v?.quality_remarks && <div>Model remarks: “{v.quality_remarks}”</div>}</div>}>
        {d.quality_score ? <Stars n={d.quality_score} /> : <span className={none}>–</span>}
        {d.keeper ? <span className="text-[11px] text-emerald-300">keeper</span> : d.keeper === false ? <span className="text-[11px] text-gray-500">cull</span> : null}
      </Item>
      {d.truth_tier != null && (
        <Item k="truth" tip={<>Ground truth you imported on the Settings page{d.truth_rating ? `, ${d.truth_rating}★` : ''}{d.truth_label ? `, ${d.truth_label} label` : ''}. The tier comes from a focus:N keyword or CSV column first, then the color label, then the star rating.</>}>
          <Tier t={d.truth_tier} />
        </Item>
      )}
      {(d.lr_rating || d.lr_label) && (
        <Item k="LR" tip={<>Read from an XMP sidecar next to the file (e.g. Lightroom): {d.lr_rating ? `${d.lr_rating}★` : 'no stars'}{d.lr_label ? `, ${d.lr_label} label` : ''}. For reference only; it doesn't affect any tier.</>}>
          <span className="text-amber-300 text-[11px]">{d.lr_rating ? '★'.repeat(d.lr_rating) : '–'}</span>{d.lr_label && <span className="text-[11px] text-gray-300">{d.lr_label}</span>}
        </Item>
      )}
      {disagree && l && <Tip tip={explainDisagree(l, d.focus_tier_vlm!, v?.focus_notes, cfg)} className="text-[11px] text-amber-300">local ≠ model</Tip>}
      {l?.split && <Tip tip={explainSplit(l.split, cfg)} className="text-[11px] text-amber-300">metrics split ({splitShort(l.split)})</Tip>}
    </div>
  );
}
