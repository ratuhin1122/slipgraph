import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import DOMPurify from 'dompurify';
import { api, dataOrThrow } from './api/client';
import { useSession } from './auth-context';

function text(value: string) { return DOMPurify.sanitize(value, { ALLOWED_TAGS: [], ALLOWED_ATTR: [] }); }
function internalURL(value: string) { return /^\/(projects|chat|meetings)\/[0-9a-f-]{36}\?/.test(value) ? value : '/projects'; }
export function SearchPalette() {
  const session = useSession(); const [open, setOpen] = useState(false); const [mode, setMode] = useState<'search' | 'ask'>('search'); const [query, setQuery] = useState(''); const [id, setId] = useState('');
  const dialog = useRef<HTMLDialogElement>(null); const input = useRef<HTMLInputElement>(null);
  useEffect(() => { function key(event: KeyboardEvent) { if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); setOpen(value => !value); } } window.addEventListener('keydown', key); return () => window.removeEventListener('keydown', key); }, []);
  useEffect(() => { if (open) { dialog.current?.showModal(); input.current?.focus(); } else if (dialog.current?.open) dialog.current.close(); }, [open]);
  const request = useMutation({ mutationFn: async () => dataOrThrow(mode === 'ask' ? await api.POST('/ask', { body: { query } }) : await api.POST('/search', { body: { query } })), onMutate: () => setId(''), onSuccess: value => setId(value.id) });
  const result = useQuery({ queryKey: [session.workspace_id, 'search', id], queryFn: async () => dataOrThrow(await api.GET('/search/{requestId}', { params: { path: { requestId: id } } })), enabled: !!id && open, refetchInterval: state => ['complete', 'failed'].includes(state.state.data?.status ?? '') ? false : 700, retry: false });
  const busy = request.isPending || (!!id && !['complete', 'failed'].includes(result.data?.status ?? '') && !result.error);
  function submit(event: FormEvent) { event.preventDefault(); if (query.trim() && !busy) request.mutate(); }
  const items = result.data?.items ?? []; const claims = result.data?.claims ?? [];
  return <><button className="secondary" onClick={() => setOpen(true)}>Search / Ask <kbd>Ctrl+K</kbd></button><dialog ref={dialog} onClose={() => setOpen(false)} className="m-auto max-h-[85vh] w-[min(48rem,95vw)] overflow-y-auto rounded-xl border border-slate-700 bg-slate-950 p-6 text-slate-100 backdrop:bg-black/70" aria-label="Workspace search">
    <div className="mb-4 flex items-center justify-between"><h2 className="text-xl font-semibold">Search your workspace</h2><button aria-label="Close search" onClick={() => setOpen(false)}>✕</button></div>
    <form onSubmit={submit} className="grid gap-3"><div className="flex gap-4"><label><input type="radio" name="search-mode" checked={mode === 'search'} onChange={() => { setMode('search'); setId(''); request.reset(); }} /> Search</label><label><input type="radio" name="search-mode" checked={mode === 'ask'} onChange={() => { setMode('ask'); setId(''); request.reset(); }} /> Ask</label></div><input ref={input} aria-label="Search query" placeholder={mode === 'ask' ? 'Ask a question about your workspace…' : 'Find tasks, messages and transcripts…'} value={query} onChange={event => setQuery(event.target.value)} maxLength={500} required /><button className="primary" disabled={busy || !query.trim()}>{busy ? 'Working…' : mode === 'ask' ? 'Ask with citations' : 'Search'}</button></form>
    {(request.error || result.error) && <p role="alert" className="mt-4 text-rose-300">{request.error?.message ?? result.error?.message}</p>}
    {result.data?.status === 'failed' && <p role="alert" className="mt-4">Search could not finish. A provider, quota, or source may have changed. Try again later.</p>}
    {result.data?.mock && <p className="my-3 text-amber-300">Demo mode: hashed word matching and a fixture answer, not semantic AI.</p>}
    {busy && <p role="status" className="my-3">Retrieving authorized workspace content…</p>}
    {result.data?.status === 'complete' && <section aria-label="Search results" className="mt-5 space-y-5">
      {mode === 'ask' && <section aria-label="Answer" className="panel"><h3 className="font-semibold">Answer with sources</h3>{claims.length === 0 && <p className="mt-2 text-slate-400">No supported answer. Try a more specific question.</p>}{claims.map((claim, index) => <div key={index} className="mt-3"><p className="whitespace-pre-wrap">{text(claim.text)}</p><div className="mt-2 flex flex-wrap gap-2">{claim.object_ids.map(objectId => { const source = items.find(item => item.object_id === objectId); return source ? <Link key={objectId} to={internalURL(source.url)} onClick={() => setOpen(false)} className="text-sm text-emerald-300">{text(source.title)} · {source.type}</Link> : null; })}</div></div>)}</section>}
      {items.length === 0 && <p>No matching visible objects. New content may still be indexing.</p>}
      {(['task', 'message', 'transcript_segment'] as const).map(type => { const group = items.filter(item => item.type === type); return group.length > 0 && <section key={type}><h3 className="mb-2 font-semibold">{type === 'transcript_segment' ? 'Transcript segments' : type === 'task' ? 'Tasks' : 'Messages'}</h3><ul className="space-y-2">{group.map(item => <li key={item.object_id}><Link to={internalURL(item.url)} onClick={() => setOpen(false)} className="block rounded border border-slate-800 p-3 hover:border-emerald-700"><strong>{text(item.title)}</strong><p className="mt-1 whitespace-pre-wrap text-sm text-slate-400">{text(item.snippet)}</p></Link></li>)}</ul></section>; })}
    </section>}
    <p className="mt-5 text-xs text-slate-500">Results contain up to 20 objects. Ask cites retrieved snippets; citations verify sources, not factual correctness. All content stays subject to your current permissions.</p>
  </dialog></>;
}
