import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, Navigate, useNavigate, useLocation } from 'react-router-dom';
import { api, dataOrThrow, getSession, type Session } from './api/client';

import { AuthContext, useAuth } from './auth-context';
function clearTenantCache(cache: ReturnType<typeof useQueryClient>) { cache.removeQueries({ predicate: query => query.queryKey[0] !== 'identity-session' }); }
export function AuthProvider({ children }: { children: ReactNode }) {
  const cache = useQueryClient();
  const query = useQuery({ queryKey: ['identity-session'], queryFn: getSession, retry: false, staleTime: 60_000 });
  const session = query.data ?? null;
  useEffect(() => { if (!session) return; const timer = window.setInterval(() => { void api.POST('/auth/refresh').then(result => { if (result.data) cache.setQueryData(['identity-session'], result.data); else { clearTenantCache(cache); cache.setQueryData(['identity-session'], null); } }).catch(() => { clearTenantCache(cache); cache.setQueryData(['identity-session'], null); }); }, 10 * 60_000); return () => window.clearInterval(timer); }, [cache, session]);
  const accept = (value: Session) => { clearTenantCache(cache); cache.setQueryData(['identity-session'], value); };
  return <AuthContext value={{ session, pending: query.isPending, error: query.error?.message ?? null, accept }}>{children}</AuthContext>;
}
export function Protected({ children }: { children: ReactNode }) { const auth = useAuth(); const location = useLocation(); if (auth.pending) return <p className="p-10" role="status">Loading your workspace…</p>; if (auth.error) return <p className="p-10 text-rose-300" role="alert">{auth.error}</p>; return auth.session ? children : <Navigate to="/login" replace state={{ returnTo: location.pathname + location.hash }} />; }
export function Login() {
  const auth = useAuth(); const navigate = useNavigate(); const location = useLocation(); const [signup, setSignup] = useState(location.pathname === '/signup'); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  if (auth.pending) return <p role="status" className="p-10">Loading your session…</p>;
  if (auth.session) return <Navigate to="/projects" replace />;
  async function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const data = new FormData(event.currentTarget); setBusy(true); setError(''); try {
    const email = String(data.get('email')); const password = String(data.get('password'));
    const session = signup ? dataOrThrow(await api.POST('/auth/signup', { body: { email, password, display_name: String(data.get('name')), workspace_name: String(data.get('workspace')) } })) : dataOrThrow(await api.POST('/auth/login', { body: { email, password } }));
    auth.accept(session); const state: unknown = location.state; const returnTo = typeof state === 'object' && state !== null && 'returnTo' in state && typeof state.returnTo === 'string' && state.returnTo.startsWith('/') && !state.returnTo.startsWith('//') ? state.returnTo : '/projects'; navigate(returnTo, { replace: true });
  } catch (error) { setError(error instanceof Error ? error.message : 'Could not sign in'); } finally { setBusy(false); } }
  return <main className="mx-auto max-w-md px-6 py-16"><Link to="/" className="text-lg font-bold text-emerald-400">slipgraph</Link><h1 className="mt-8 text-3xl font-semibold">{signup ? 'Create your team workspace' : 'Welcome back'}</h1><p className="mt-3 text-slate-400">Plan together. See downstream risk. Keep people in control.</p>
    <form onSubmit={event => { void submit(event); }} className="mt-8 grid gap-4">
      {signup && <><label>Your name<input name="name" autoComplete="name" required maxLength={100} /></label><label>Workspace name<input name="workspace" required maxLength={100} /></label></>}
      <label>Email<input name="email" type="email" autoComplete="email" required maxLength={254} /></label><label>Password<input name="password" type="password" autoComplete={signup ? 'new-password' : 'current-password'} required minLength={signup ? 12 : 1} maxLength={256} /></label>
      {signup && <p className="text-xs text-slate-400">Use at least 12 characters.</p>}{error && <p role="alert" className="text-rose-300">{error}</p>}
      <button className="primary" disabled={busy}>{busy ? 'Please wait…' : signup ? 'Create account' : 'Sign in'}</button>
    </form><button className="mt-6 text-sm text-emerald-400" onClick={() => { setSignup(!signup); setError(''); }}>{signup ? 'Already have an account? Sign in' : 'New here? Create an account'}</button>
  </main>;
}
export function Shell({ children }: { children: ReactNode }) {
  const { session, accept } = useAuth(); const cache = useQueryClient(); const navigate = useNavigate(); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  const workspaces = useQuery({ queryKey: [session?.workspace_id, 'workspaces', session?.user.id], queryFn: async () => dataOrThrow(await api.GET('/auth/workspaces')), enabled: !!session, staleTime: 30_000 });
  if (!session) return null;
  async function switchWorkspace(ticket: string) { setBusy(true); setError(''); try { accept(dataOrThrow(await api.POST('/auth/workspace-switch', { body: { ticket } }))); navigate('/projects'); } catch (error) { setError(error instanceof Error ? error.message : 'Switch failed'); void cache.invalidateQueries({ queryKey: [session?.workspace_id, 'workspaces'] }); } finally { setBusy(false); } }
  async function logout() { setBusy(true); try { const result = await api.POST('/auth/logout'); if (result.error) throw new Error('Sign out failed'); clearTenantCache(cache); cache.setQueryData(['identity-session'], null); navigate('/login'); } catch (error) { setError(error instanceof Error ? error.message : 'Sign out failed'); } finally { setBusy(false); } }
  async function createWorkspace() { const name = window.prompt('Name your new workspace'); if (!name) return; setBusy(true); try { accept(dataOrThrow(await api.POST('/auth/workspaces', { body: { name } }))); navigate('/projects'); } catch (error) { setError(error instanceof Error ? error.message : 'Could not create workspace'); } finally { setBusy(false); } }
  return <><header className="border-b border-slate-800 bg-slate-950"><div className="mx-auto flex max-w-[1600px] flex-wrap items-center gap-5 px-6 py-4"><Link to="/projects" className="mr-3 text-xl font-bold tracking-tight text-emerald-400">slipgraph</Link>
    <label className="flex items-center gap-2 text-sm"><span className="sr-only">Workspace</span><select aria-label="Workspace" className="max-w-56" value={session.workspace_id} disabled={busy} onChange={event => { const target = workspaces.data?.find(item => item.id === event.target.value); if (target) void switchWorkspace(target.switch_token); }}>{(workspaces.data ?? [{ id: session.workspace_id, name: session.workspace_name }]).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
    <Link to="/projects" className="nav-link">Projects</Link><Link to="/chat">Chat</Link><Link to="/members" className="nav-link">Members</Link><button className="nav-link" disabled={busy} onClick={() => { void createWorkspace(); }}>New workspace</button><span className="ml-auto text-sm text-slate-400">{session.user.display_name} · {session.role}</span><button className="secondary" disabled={busy} onClick={() => { void logout(); }}>Sign out</button>
  </div></header>{error && <p className="mx-auto max-w-6xl p-4 text-rose-300" role="alert">{error}</p>}<div key={session.workspace_id}>{children}</div></>;
}
