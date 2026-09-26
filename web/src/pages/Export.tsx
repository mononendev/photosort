import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import type { ExportResult } from '../api/client';

export default function Export() {
  const [name, setName] = useState('export');
  const [folder, setFolder] = useState('');
  const [link, setLink] = useState('copy');
  const [xmp, setXmp] = useState(true);
  const [tree, setTree] = useState(true);
  const [source, setSource] = useState('vlm');
  const [result, setResult] = useState<ExportResult | null>(null);
  const { data: exports, refetch } = useQuery({ queryKey: ['exports'], queryFn: api.exports });
  const run = useMutation({
    mutationFn: () => api.exportRun({ name, folder, link, xmp, tree, focus_source: source }),
    onSuccess: (r) => { setResult(r); refetch(); },
  });
  const sel = 'bg-gray-900 border border-gray-700 rounded px-2 py-1.5 sm:py-1 text-sm mb-2 sm:mb-0';
  return (
    <div className="max-w-2xl space-y-4">
      <h1 className="text-lg font-semibold">Export</h1>
      <p className="text-sm text-gray-400">Writes <code>results.csv</code> / <code>results.jsonl</code>, XMP sidecars (keywords, description, rating, hierarchical keywords) and a sorted tree <code>focus_N/subject/composition/</code> under the data volume's <code>exports/&lt;name&gt;</code>. The photos volume is read-only, so the tree copies (or links) files rather than moving them.</p>
      <div className="grid grid-cols-1 sm:grid-cols-[140px_1fr] gap-x-3 gap-y-1.5 sm:gap-y-3 sm:items-center text-sm">
        <span className="text-gray-500">name</span><input value={name} onChange={(e) => setName(e.target.value)} className={sel} />
        <span className="text-gray-500">folder filter</span><input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="(all) relative to photos root" className={sel} />
        <span className="text-gray-500">tree files</span>
        <select value={link} onChange={(e) => setLink(e.target.value)} className={sel}><option value="copy">copy</option><option value="symlink">symlink</option><option value="hardlink">hardlink (same filesystem only)</option></select>
        <span className="text-gray-500">focus source</span>
        <select value={source} onChange={(e) => setSource(e.target.value)} className={sel}><option value="vlm">vision model (your overrides win)</option><option value="local">local sharpness only</option><option value="strict">strict: lower of both</option></select>
        <span className="text-gray-500">options</span>
        <span className="flex flex-wrap gap-4"><label className="flex items-center gap-1"><input type="checkbox" checked={xmp} onChange={(e) => setXmp(e.target.checked)} /> XMP sidecars</label><label className="flex items-center gap-1"><input type="checkbox" checked={tree} onChange={(e) => setTree(e.target.checked)} /> sorted tree</label></span>
      </div>
      <button onClick={() => run.mutate()} disabled={run.isPending} className="w-full sm:w-auto px-4 py-2.5 sm:py-1.5 rounded-md bg-blue-600 hover:bg-blue-500 active:bg-blue-700 active:scale-[0.98] transition disabled:opacity-40 text-sm font-medium">{run.isPending ? 'Exporting…' : 'Run export'}</button>
      {run.error && <p className="text-sm text-red-400">{(run.error as Error).message}</p>}
      {result && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-3 text-sm">
          <div>Wrote <b>{result.images}</b> records to <code className="break-all">{result.out}</code>{result.xmp_written ? `, ${result.xmp_written} XMP files` : ''}.</div>
          {Object.keys(result.tree).length > 0 && <div className="text-gray-400 mt-1">{Object.entries(result.tree).map(([k, v]) => `${k}: ${v}`).join(' · ')}</div>}
        </div>
      )}
      {exports && exports.length > 0 && (
        <div>
          <h2 className="text-sm font-semibold text-gray-300 mb-1">Previous exports</h2>
          <ul className="text-sm text-gray-400 space-y-0.5">{exports.map((e) => <li key={e.name} className="break-all"><code>{e.path}</code> · {new Date(e.mtime * 1000).toLocaleString()}</li>)}</ul>
        </div>
      )}
    </div>
  );
}
