import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import { MessageBody, type ChatMessage } from './MessageBody';
import { reconnectDelay } from './chat-realtime';
import { Chat } from './Chat';
import { AuthContext } from './auth-context';
import type { Session } from './api/client';
vi.mock('./chat-realtime', async importOriginal => ({ ...await importOriginal<typeof import('./chat-realtime')>(), useChatRealtime: () => ({ connected: true, typing: vi.fn() }) }));
const id = '019a1111-1111-7111-8111-111111111111';
const message: ChatMessage = { id, channel_id: id, author_id: id, author_name: 'A', body: '', thread_id: null, client_id: id, version: 1, created_at: '2030-01-01T00:00:00Z', updated_at: '2030-01-01T00:00:00Z', deleted: false, reply_count: 0, mentions: [], task_links: [] };
test('XSS payloads and external media remain inert, including encoded markup', () => {
  const body = '<img src="https://evil.example/track" onerror="alert(1)"><script>alert(1)</script><svg onload="alert(1)"></svg><a href="javascript:alert(1)">hello</a> &lt;img src=x onerror=alert(1)&gt;';
  const { container } = render(<MemoryRouter><MessageBody message={{ ...message, body }} /></MemoryRouter>);
  expect(container.querySelector('img,script,svg,iframe,a')).toBeNull(); expect(container.textContent).toContain('hello'); expect(container.textContent).toContain('<img src=x onerror=alert(1)>');
});
test('only server-validated task links produce internal task cards', () => {
  const { container } = render(<MemoryRouter><MessageBody message={{ ...message, body: 'slipgraph://task/' + id + ' https://evil.example', task_links: [{ id, project_id: id, title: '<img src=x onerror=alert(1)>', status: 'todo' }] }} /></MemoryRouter>);
  expect(screen.getByRole('link')).toHaveAttribute('href', '/projects/' + id + '?task=' + id); expect(container.querySelector('img')).toBeNull(); expect(screen.getByRole('link')).toHaveTextContent('<img');
});
test('reconnect backoff grows and stays bounded', () => { expect(reconnectDelay(0, 0)).toBe(500); expect(reconnectDelay(4, 0)).toBe(8000); expect(reconnectDelay(100, 0.5)).toBe(30150); });
test('optimistic failed sends retain the same idempotency key on retry', async () => {
  const session: Session = { id, user: { id, display_name: 'A', email: 'a@example.com' }, workspace_id: id, workspace_name: 'A', role: 'admin', expires_at: '2030-01-01T00:15:00Z' };
  const attempts: string[] = []; let stored: ChatMessage[] = [];
  const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
  vi.mocked(fetch).mockImplementation(async input => { const request = input instanceof Request ? input : new Request(input); const path = new URL(request.url).pathname;
    if (path === '/api/auth/csrf') return json({ csrf_token: 'x'.repeat(43) });
    if (path === '/api/channels') return json([{ id, name: 'general', version: 1 }]);
    if (path === '/api/channels/' + id) return json({ id, name: 'general', version: 1 });
    if (path.endsWith('/presence') || path === '/api/members' || path === '/api/projects') return json([]);
    if (path.endsWith('/messages') && request.method === 'POST') { const body = await request.json() as { body: string; client_id: string }; attempts.push(body.client_id); if (attempts.length === 1) return json({ error: 'temporary failure' }, 503); stored = [{ ...message, id: body.client_id, client_id: body.client_id, body: body.body }]; return json(stored[0]); }
    if (path.endsWith('/messages')) return json({ items: stored, next_cursor: null });
    return json({ error: 'not found' }, 404);
  });
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(<QueryClientProvider client={cache}><AuthContext value={{ session, pending: false, error: null, accept: vi.fn() }}><MemoryRouter initialEntries={['/chat/' + id]}><Routes><Route path="/chat/:channelId" element={<Chat />} /></Routes></MemoryRouter></AuthContext></QueryClientProvider>);
  const composer = await screen.findByLabelText('Message'); fireEvent.change(composer, { target: { value: 'optimistic message' } }); fireEvent.click(screen.getByRole('button', { name: 'Send' }));
  expect(await screen.findByText('optimistic message')).toBeInTheDocument(); fireEvent.click(await screen.findByRole('button', { name: 'Retry send' }));
  await waitFor(() => expect(attempts).toHaveLength(2)); expect(attempts[0]).toBe(attempts[1]); await waitFor(() => expect(screen.queryByRole('button', { name: 'Retry send' })).not.toBeInTheDocument()); expect(screen.getAllByText('optimistic message')).toHaveLength(1);
});
