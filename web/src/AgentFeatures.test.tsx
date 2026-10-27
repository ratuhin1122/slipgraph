import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import { AgentNotice, ImpactPanel, Plans, type AgentRun } from './AgentFeatures';
import { AuthContext } from './auth-context';
import type { Session } from './api/client';
const id = '019a1111-1111-7111-8111-111111111111';
const other = '019a2222-2222-7222-8222-222222222222';
const session: Session = { id, user: { id, display_name: 'A', email: 'a@example.com' }, workspace_id: id, workspace_name: 'A', role: 'admin', expires_at: '2035-01-01T00:00:00Z' };
const run: AgentRun = { id, kind: 'delay_detection', status: 'needs_confirmation', requested_by: id, result: { candidates: [{ id: other, title: 'Backend API' }], proposal_ids: [], graph_version: 1, launch_flagged: false, changes: [], detection: { is_delay: true, days: 3, confidence: 0.4, task_candidates: ['Backend API'], evidence_quote: '<img src="https://evil.example" onerror="alert(1)"> maybe backend API will slip 3 days' } } };
function mount(children: React.ReactNode) { const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }); return render(<QueryClientProvider client={cache}><AuthContext value={{ session, pending: false, error: null, accept: vi.fn() }}><MemoryRouter>{children}</MemoryRouter></AuthContext></QueryClientProvider>); }
const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } });
test('low-confidence card requires explicit task/day confirmation and renders evidence inert', async () => {
 let confirmed = false;
 vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); const path = new URL(request.url).pathname;
 if (path.endsWith('/csrf')) return json({ csrf_token: 'x'.repeat(43) });
 if (path.endsWith('/confirm')) { expect(request.headers.get('X-CSRF-Token')).toBe('x'.repeat(43)); expect(await request.json()).toEqual({ task_id: other, days: 3 }); confirmed = true; return json({ ...run, status: 'completed' }); }
 return json({ ...run, status: confirmed ? 'completed' : 'needs_confirmation' }); });
 const { container } = mount(<AgentNotice runId={id} />);
 await screen.findByText('Confirm a possible delay'); expect(container.querySelector('img,script,iframe')).toBeNull(); expect(confirmed).toBe(false);
 fireEvent.click(screen.getByRole('button', { name: 'Confirm delay and preview impact' })); await waitFor(() => expect(confirmed).toBe(true));
});
test('impact panel shows date changes, milestone warning, and deterministic explanations', () => {
 const scenario: AgentRun = { ...run, result: { ...run.result, launch_flagged: true, proposal_ids: [other], changes: [{ task_id: other, title: 'Launch', before_start: '2030-01-03T00:00:00Z', before_due: '2030-01-03T00:00:00Z', after_start: '2030-01-06T00:00:00Z', after_due: '2030-01-06T00:00:00Z', milestone: true, reason: 'A dependency finishes later; zero slack remains.' }] } };
 mount(<ImpactPanel run={scenario} graphVersion={2} />); expect(screen.getAllByText('2030-01-03 → 2030-01-06')).toHaveLength(2); expect(screen.getByText('A dependency finishes later; zero slack remains.')).toBeInTheDocument(); expect(screen.getByText(/project has changed/)).toBeInTheDocument(); expect(screen.getByRole('link')).toHaveAttribute('href', '/proposals#' + other);
});
test('brief submits to a queued run and links to one reviewable plan', async () => {
 let posted = false;
 vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); const path = new URL(request.url).pathname;
 if (path.endsWith('/settings')) return json({ enabled: true, mode: 'mock' });
 if (path.endsWith('/csrf')) return json({ csrf_token: 'x'.repeat(43) });
 if (path.endsWith('/plans')) { expect(await request.json()).toEqual({ brief: 'Launch after the API is ready.' }); posted = true; return json({ id }); }
 return json({ ...run, kind: 'create_plan', status: 'completed', result: { ...run.result, proposal_ids: [other] } }); });
 mount(<Plans />); await waitFor(() => expect(screen.getByRole('button', { name: 'Propose a plan' })).toBeEnabled()); fireEvent.change(screen.getByLabelText('Project brief'), { target: { value: 'Launch after the API is ready.' } }); fireEvent.click(screen.getByRole('button', { name: 'Propose a plan' })); expect(await screen.findByRole('link', { name: 'Review the complete plan' })).toHaveAttribute('href', '/proposals#' + other); expect(posted).toBe(true);
});
