import { render, screen, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import App from './App';
import type { Session } from './api/client';
const session: Session = { id: 'session-a', user: { id: 'user-a', email: 'a@example.com', display_name: 'A' }, workspace_id: 'workspace-a', workspace_name: 'A', role: 'admin', expires_at: '2030-01-01T00:15:00Z' };
function json(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }); }
function app(path = '/') { const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }); return render(<QueryClientProvider client={cache}><MemoryRouter initialEntries={[path]}><App /></MemoryRouter></QueryClientProvider>); }
test('sign in uses session-bound CSRF and opens only the authenticated workspace', async () => {
  let authenticated = false;
  vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); const path = new URL(request.url).pathname;
    if (path === '/api/auth/csrf') return json({ csrf_token: 'x'.repeat(43) });
    if (path === '/api/auth/session') return authenticated ? json(session) : json({ error: 'authentication required' }, 401);
    if (path === '/api/auth/refresh') return json({ error: 'authentication required' }, 401);
    if (path === '/api/auth/login') { expect(request.headers.get('X-CSRF-Token')).toBe('x'.repeat(43)); const body: unknown = await request.json(); expect(body).toEqual({ email: 'a@example.com', password: 'correct horse battery staple' }); authenticated = true; return json(session); }
    if (path === '/api/auth/workspaces') return json([{ id: session.workspace_id, name: 'A', role: 'admin', switch_token: 's'.repeat(43) }]);
    if (path === '/api/projects') return json([]);
    return json({ error: 'not found' }, 404);
  });
  app('/login'); await screen.findByRole('heading', { name: 'Welcome back' });
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'a@example.com' } }); fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse battery staple' } }); fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  expect(await screen.findByRole('heading', { name: 'Your projects' })).toBeInTheDocument(); expect(await screen.findByLabelText('Workspace')).toHaveValue('workspace-a');
});
test('unavailable identity service fails closed', async () => { vi.mocked(fetch).mockResolvedValue(json({ error: 'internal error' }, 500)); app('/projects'); expect(await screen.findByRole('alert')).toHaveTextContent('internal error'); expect(screen.queryByRole('heading', { name: 'Your projects' })).not.toBeInTheDocument(); });
test('an unauthenticated invite redirects to login and preserves its fragment', async () => { vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); return new URL(request.url).pathname.endsWith('/csrf') ? json({ csrf_token: 'x'.repeat(43) }) : json({ error: 'authentication required' }, 401); }); app('/invite#token=' + 't'.repeat(43)); expect(await screen.findByRole('heading', { name: 'Welcome back' })).toBeInTheDocument(); });
