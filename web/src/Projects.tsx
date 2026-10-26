import { useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { api, dataOrThrow } from './api/client';
import { useSession } from './auth-context';
export function Projects() {
  const session = useSession(); const navigate = useNavigate(); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  const query = useQuery({ queryKey: [session.workspace_id, 'projects'], queryFn: async () => dataOrThrow(await api.GET('/projects')) });
  async function create(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); const due = String(form.get('launch_due')); setBusy(true); setError(''); try {
    const project = dataOrThrow(await api.POST('/projects', { body: { name: String(form.get('name')), description: String(form.get('description')), start: String(form.get('start')) + 'T00:00:00Z', launch_due: due ? due + 'T00:00:00Z' : null } })); navigate('/projects/' + project.id);
  } catch (error) { setError(error instanceof Error ? error.message : 'Could not create project'); } finally { setBusy(false); } }
  return <main className="mx-auto max-w-6xl p-6"><div className="mb-8"><p className="eyebrow">{session.workspace_name}</p><h1 className="page-title mt-2">Your projects</h1><p className="mt-3 text-slate-400">Connect tasks, understand what changes, and review risk together.</p></div>
    {query.isPending ? <p role="status">Loading projects…</p> : query.error ? <p role="alert">{query.error.message}</p> : query.data?.length === 0 ? <div className="panel text-slate-400">Your first project starts here.</div> : <div className="grid gap-4 md:grid-cols-2">{query.data?.map(project => <Link className="panel transition hover:border-emerald-600" key={project.id} to={'/projects/' + project.id}><h2 className="text-xl font-semibold">{project.name}</h2><p className="mt-2 whitespace-pre-wrap text-slate-400">{project.description || 'No description yet.'}</p><p className="mt-4 text-sm text-slate-500">Starts {project.start.slice(0, 10)}{project.launch_due ? ' · Launch ' + project.launch_due.slice(0, 10) : ''}</p></Link>)}</div>}
    {session.role !== 'viewer' && <section className="panel mt-8"><h2 className="text-lg font-semibold">Create a project</h2><form className="mt-4 grid gap-4 md:grid-cols-2" onSubmit={event => { void create(event); }}><label>Name<input name="name" required maxLength={200} /></label><label>Start (UTC)<input name="start" type="date" defaultValue={new Date().toISOString().slice(0, 10)} required /></label><label>Description<textarea name="description" maxLength={4000} /></label><label>Launch due (UTC, optional)<input name="launch_due" type="date" /></label>{error && <p role="alert" className="text-rose-300 md:col-span-2">{error}</p>}<button className="primary justify-self-start" disabled={busy}>Create project</button></form></section>}
  </main>;
}
