import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import type { DetectResult, LocalResult } from '../api/client';
import useStore from '../hooks/useStore';
import Tip from './Tip';

const LEGACY = 'yolo11n-pose';

/**
 * Pick another pose model to run on this photo and draw its detections (cyan) over the stored ones, to see whether a
 * missed or ghost person is the model's doing. Nothing is stored; the choice sticks while stepping through photos.
 */
export default function CompareBar({ l, result, loading, error }: {
  l: LocalResult; result: DetectResult | null; loading: boolean; error?: string;
}) {
  const { data: models } = useQuery({ queryKey: ['models'], queryFn: api.models, staleTime: 60_000 });
  const model = useStore((s) => s.compareModel);
  const setModel = useStore((s) => s.setCompareModel);
  const stored = l.detector ?? LEGACY;
  const installed = models?.installed ?? [];
  if (installed.length === 0) return null;
  const found = l.mask_boxes?.length ?? l.n_people;
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <Tip tip={<>The people on this photo were found by <b>{stored}</b>. Pick another pose model to run it on this frame and draw what it finds in cyan, dashed; nothing is stored. To analyze photos with a model, set it on the Calibrate page and re-analyze them.</>}>
        <span className="text-gray-500">compare detector</span>
      </Tip>
      <select value={model ?? ''} onChange={(e) => setModel(e.target.value || null)}
        className="bg-gray-900 border border-gray-700 rounded px-1.5 py-0.5 text-gray-200">
        <option value="">off</option>
        {installed.map((m) => <option key={m.name} value={m.name}>{m.name}{m.name === stored ? ' (stored)' : ''}</option>)}
      </select>
      {model && (loading ? <span className="text-gray-500">running {model}…</span>
        : error ? <span className="text-red-400">{error}</span>
          : result && <span className="text-cyan-300">
            {result.people.length} {result.people.length === 1 ? 'person' : 'people'} vs {found} stored ({stored}) · {result.seconds.toFixed(2)} s
          </span>)}
    </div>
  );
}
