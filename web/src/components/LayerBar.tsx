import type { FocusDebug } from '../api/client';
import Tip from './Tip';
import { LAYERS } from '../lib/pose';
import type { Layer } from '../lib/pose';

/** Overlay layer toggles, plus the sharpness-map legend when that layer is on. */
export default function LayerBar({ layers, toggle, heat, heatLoading }: {
  layers: Set<Layer>; toggle: (k: Layer) => void; heat?: FocusDebug['heatmap']; heatLoading?: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-1 text-xs">
      {LAYERS.map((ly) => (
        <Tip key={ly.key} plain tip={ly.tip}>
          <button onClick={() => toggle(ly.key)} className={`px-2 py-0.5 rounded border ${layers.has(ly.key) ? 'border-blue-500 bg-blue-900/40 text-gray-100' : 'border-gray-700 text-gray-500 hover:border-gray-500'}`}>{ly.label}</button>
        </Tip>
      ))}
      {layers.has('heatmap') && (heatLoading ? <span className="text-gray-500 ml-1">computing…</span> : heat && (
        <span className="ml-2 inline-flex items-center gap-1 text-[10px] text-gray-500">
          soft <span className="inline-block h-2 w-20 rounded" style={{ background: 'linear-gradient(90deg,#30123b,#4686fb,#1ae4b6,#a2fc3c,#faba39,#e4460a,#7a0403)' }} /> sharp
          <span className="font-mono">(log₁₀ {heat.log_range[0]}…{heat.log_range[1]}, {heat.tile}px tiles)</span>
        </span>
      ))}
    </div>
  );
}
