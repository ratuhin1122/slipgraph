import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import { Proposals } from './Proposals';
import { AuthContext } from './auth-context';
import type { components } from './api/schema.gen';
import type { Session } from './api/client';
const id = '019a1111-1111-7111-8111-111111111111';
const session: Session = { id, user: { id, display_name: 'A', email: 'a@example.com' }, workspace_id: id, workspace_name: 'A', role: 'admin', expires_at: '2035-01-01T00:00:00Z' };
const proposal: components['schemas']['Proposal'] = { id, kind: 'reschedule_task', payload: {}, payload_hash: 'a'.repeat(64), approval_hash: null, status: 'pending', created_by_agent: id, requested_by_user: id, approved_by: null, expires_at: '2099-01-01T00:00:00Z', executed_at: null, source_object_ids: [id], source_links: [{ id, label: 'task', href: '/projects/' + id + '?task=' + id }], before: { task_id: id, due: '2030-01-01' }, after: { task_id: id, due: '2030-01-02', title: '<img src="https://evil.example" onerror="alert(1)"> text' }, version: 1, rejection_reason: null, failure_code: null };
function mount(role: Session['role'] = 'admin') { const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }); return render(<QueryClientProvider client={cache}><AuthContext value={{ session: { ...session, role }, pending: false, error: null, accept: vi.fn() }}><MemoryRouter><Proposals /></MemoryRouter></AuthContext></QueryClientProvider>); }
function json(value: unknown) { return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } }); }
test('inbox renders inert diffs, internal sources, and approves the displayed version/hash with CSRF', async () => {
 let approved = false; vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); const path = new URL(request.url).pathname;
 if (path === '/api/auth/csrf') return json({ csrf_token: 'x'.repeat(43) });
 if (path.endsWith('/approve')) { expect(request.headers.get('X-CSRF-Token')).toBe('x'.repeat(43)); expect(await request.json()).toEqual({ version: 1, payload_hash: proposal.payload_hash }); approved = true; return json({ ...proposal, status: 'approved' }); }
 return json({ items: [{ ...proposal, status: approved ? 'approved' : 'pending' }], next_cursor: null }); });
 const { container } = mount(); await screen.findByText('reschedule task'); expect(container.querySelector('img,script,iframe')).toBeNull(); expect(screen.getByRole('link')).toHaveAttribute('href', '/projects/' + id + '?task=' + id); expect(screen.getByText('2030-01-02', { exact: false })).toBeInTheDocument();
 fireEvent.click(screen.getByRole('button', { name: 'Approve these changes' })); await waitFor(() => expect(approved).toBe(true)); expect(await screen.findByText('Approved and awaiting execution.')).toBeInTheDocument();
});
test('viewer cannot approve and external source URLs never become links', async () => { vi.mocked(fetch).mockResolvedValue(json({ items: [{ ...proposal, source_links: [{ id, label: 'malicious', href: 'javascript:alert(1)' }] }], next_cursor: null })); mount('viewer'); await screen.findByText('reschedule task'); expect(screen.queryByRole('button', { name: 'Approve these changes' })).not.toBeInTheDocument(); expect(screen.queryByRole('link')).not.toBeInTheDocument(); });
test('reject requires a reason and pins the same reviewed hash', async () => { let rejected = false; vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); const path = new URL(request.url).pathname; if (path === '/api/auth/csrf') return json({ csrf_token: 'x'.repeat(43) }); if (path.endsWith('/reject')) { expect(await request.json()).toEqual({ version: 1, payload_hash: proposal.payload_hash, reason: 'Need evidence' }); rejected = true; return json({ ...proposal, status: 'rejected' }); } return json({ items: [proposal], next_cursor: null }); }); mount(); await screen.findByText('reschedule task'); expect(screen.getByRole('button', { name: 'Reject' })).toBeDisabled(); fireEvent.change(screen.getByLabelText('Reason for rejecting'), { target: { value: 'Need evidence' } }); fireEvent.click(screen.getByRole('button', { name: 'Reject' })); await waitFor(() => expect(rejected).toBe(true)); });
