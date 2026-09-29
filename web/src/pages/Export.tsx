import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, GROUPS } from '../api/client';
import type { ExportResult, GroupsConfig, XMPFormat } from '../api/client';
import { errMsg, fmtTime } from '../lib/format';

export default function Export() {
  const [name, setName] = useState('export');
  const [folder, setFolder] = useState('');
  const [link, setLink] = useState('copy');
  const [xmp, setXmp] = useState(true);
  const [tree, setTree] = useState(true);
  const [source, setSource] = useState('vlm');
  const [result, setResult] = useState<ExportResult | null>(null);
  const [xmpFormat, setXmpFormat] = useState<XMPFormat>('capture_one');
  const { data: exports, refetch } = useQuery({ queryKey: ['exports'], queryFn: api.exports });
  // Your sort groups' export folder and keywords live in the config; edits are drafts until saved (or exported).
  const qc = useQueryClient();
  const { data: cfg } = useQuery({ queryKey: ['config'], queryFn: api.config });
  const saved = (cfg?.groups ?? {}) as GroupsConfig;
  const [draft, setDraft] = useState<Record<string, { folder?: string; keywords?: string }>>({});
  const folderOf = (g: number) => draft[g]?.folder ?? saved[g]?.folder ?? `group_${g}`;
  const keywordsOf = (g: number) => draft[g]?.keywords ?? (saved[g]?.keywords ?? []).join(', ');
  const edit = (g: number, k: 'folder' | 'keywords', v: string) => setDraft((d) => ({ ...d, [g]: { ...d[g], [k]: v } }));
  const dirty = Object.keys(draft).length > 0;
  const saveGroups = useMutation({
    mutationFn: () => api.putConfig({ groups: Object.fromEntries(GROUPS.map(({ value: g }) => [g, {
      folder: folderOf(g).trim() || `group_${g}`, keywords: keywordsOf(g).split(',').map((k) => k.trim()).filter(Boolean),
    }])) }, 'export groups'),
    onSuccess: (c) => { qc.setQueryData(['config'], c); setDraft({}); },
  });
  const run = useMutation({
    mutationFn: async () => {
      if (dirty) await saveGroups.mutateAsync();
      return api.exportRun({ name, folder, link, xmp, tree, focus_source: source });
    },
    onSuccess: (r) => { setResult(r); refetch(); },
  });
  const zip = useMutation({
    mutationFn: async () => {
      if (dirty) await saveGroups.mutateAsync();
      return api.xmpZip({ folder, format: xmpFormat, focus_source: source });
    },
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
      <div className="rounded-lg border border-gray-800 p-3 space-y-2">
        <div className="text-xs uppercase tracking-wide text-gray-500">XMP sidecars zip</div>
        <p className="text-xs text-gray-500">Downloads a <code>&lt;name&gt;.xmp</code> for each analyzed photo under the folder filter, in folders as they are under it (e.g. filter <code>Fest/2024</code> gives <code>September/12/…</code>). Unzip it into that folder and each sidecar lands next to its raw. Uses the focus source above.</p>
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <span className="inline-flex rounded border border-gray-700 overflow-hidden">
            {([['capture_one', 'Capture One'], ['lightroom', 'Lightroom']] as const).map(([v, l]) => (
              <button key={v} onClick={() => setXmpFormat(v)} aria-pressed={xmpFormat === v} className={`px-3 py-1 ${xmpFormat === v ? 'bg-gray-700 text-white' : 'text-gray-400 hover:text-gray-200'}`}>{l}</button>
            ))}
          </span>
          <button onClick={() => zip.mutate()} disabled={zip.isPending} className="px-3 py-1 rounded border border-gray-700 hover:border-gray-500 disabled:opacity-40">{zip.isPending ? 'building zip…' : 'Download XMP zip'}</button>
          {zip.data && !zip.isPending && <span className="text-gray-500 break-all">saved {zip.data}</span>}
        </div>
        {zip.error && <p className="text-sm text-red-400">{errMsg(zip.error)}</p>}
      </div>
      <div className="rounded-lg border border-gray-800 p-3 space-y-2">
        <div className="text-xs uppercase tracking-wide text-gray-500">Your groups</div>
        <p className="text-xs text-gray-500">Photos you put in a group (keys a s d f in the photo view) sort into that group's folder in the tree, as <code>&lt;folder&gt;/focus_N/subject/composition/</code>, instead of the top level. Their XMP sidecars get the group's keywords; separate them with commas, and use <code>Parent|Child</code> for a hierarchical keyword.</p>
        {GROUPS.map(({ value: g, key }) => (
          <div key={g} className="grid grid-cols-[3.5rem_1fr] sm:grid-cols-[3.5rem_10rem_1fr] gap-x-2 gap-y-1 items-center text-sm">
            <span className="text-gray-500">{g} <kbd className="text-[10px]">{key}</kbd></span>
            <input value={folderOf(g)} onChange={(e) => edit(g, 'folder', e.target.value)} placeholder={`group_${g}`} aria-label={`Group ${g} folder`} className={sel} />
            <input value={keywordsOf(g)} onChange={(e) => edit(g, 'keywords', e.target.value)} placeholder="keywords, comma separated" aria-label={`Group ${g} keywords`} className={`${sel} col-start-2 sm:col-start-auto`} />
          </div>
        ))}
        {dirty && <button onClick={() => saveGroups.mutate()} disabled={saveGroups.isPending} className="px-3 py-1 rounded border border-gray-700 hover:border-gray-500 text-sm disabled:opacity-40">{saveGroups.isPending ? 'saving…' : 'save groups'}</button>}
        {saveGroups.error && <p className="text-sm text-red-400">{errMsg(saveGroups.error)}</p>}
      </div>
      <button onClick={() => run.mutate()} disabled={run.isPending} className="w-full sm:w-auto px-4 py-2.5 sm:py-1.5 rounded-md bg-blue-600 hover:bg-blue-500 active:bg-blue-700 active:scale-[0.98] transition disabled:opacity-40 text-sm font-medium">{run.isPending ? 'Exporting…' : 'Run export'}</button>
      {run.error && <p className="text-sm text-red-400">{errMsg(run.error)}</p>}
      {result && (
        <div className="rounded-lg border border-gray-800 bg-gray-900 p-3 text-sm">
          <div>Wrote <b>{result.images}</b> records to <code className="break-all">{result.out}</code>{result.xmp_written ? `, ${result.xmp_written} XMP files` : ''}.</div>
          {Object.keys(result.tree).length > 0 && <div className="text-gray-400 mt-1">{Object.entries(result.tree).map(([k, v]) => `${k}: ${v}`).join(' · ')}</div>}
        </div>
      )}
      {exports && exports.length > 0 && (
        <div>
          <h2 className="text-sm font-semibold text-gray-300 mb-1">Previous exports</h2>
          <ul className="text-sm text-gray-400 space-y-0.5">{exports.map((e) => <li key={e.name} className="break-all"><code>{e.path}</code> · {fmtTime(e.mtime)}</li>)}</ul>
        </div>
      )}
    </div>
  );
}
