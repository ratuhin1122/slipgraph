import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import { SearchPalette } from './Search';
import { AuthContext } from './auth-context';
import type { Session } from './api/client';

const id = '019a1111-1111-7111-8111-111111111111';
const session: Session = { id, user: { id, display_name: 'A', email: 'a@example.com' }, workspace_id: id, workspace_name: 'A', role: 'admin', expires_at: '2035-01-01T00:00:00Z' };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
test('Ctrl+K opens search, queues CSRF-protected Ask and renders inert cited results', async () => {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: function (this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: function (this: HTMLDialogElement) { this.removeAttribute('open'); } });
  let asked = false;
  vi.mocked(fetch).mockImplementation(async input => {
    const req = input instanceof Request ? input : new Request(input); const url = new URL(req.url);
    if (url.pathname.endsWith('/csrf')) return json({ csrf_token: 'x'.repeat(43) });
    if (req.method === 'POST') { expect(url.pathname).toBe('/api/ask'); expect(req.headers.get('X-CSRF-Token')).toBe('x'.repeat(43)); expect(await req.json()).toEqual({ query: 'What is delayed?' }); asked = true; return json({ id, status: 'queued', items: [], claims: [], mock: true }, 202); }
    return json({ id, status: 'complete', mock: true, items: [{ object_id: id, type: 'task', title: '<img src="https://evil.example" onerror="alert(1)">Launch', snippet: '<script>alert(1)</script>Three days', url: '/projects/' + id + '?task=' + id, content_hash: 'a'.repeat(64) }], claims: [{ text: '<iframe src="https://evil.example"></iframe>Launch is delayed.', object_ids: [id, 'forged-id'] }] });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const { container } = render(<QueryClientProvider client={client}><AuthContext value={{ session, pending: false, error: null, accept: vi.fn() }}><MemoryRouter><SearchPalette /></MemoryRouter></AuthContext></QueryClientProvider>);
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true }); await screen.findByRole('dialog', { name: 'Workspace search' });
  fireEvent.click(screen.getByRole('radio', { name: 'Ask' })); fireEvent.change(screen.getByLabelText('Search query'), { target: { value: 'What is delayed?' } }); fireEvent.click(screen.getByRole('button', { name: 'Ask with citations' }));
  await screen.findByText('Launch is delayed.'); await waitFor(() => expect(asked).toBe(true));
  expect(container.querySelector('img,script,iframe')).toBeNull(); expect(screen.queryByText('forged-id')).toBeNull();
  expect(screen.getByRole('link', { name: 'Launch · task' })).toHaveAttribute('href', '/projects/' + id + '?task=' + id);
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true }); await waitFor(() => expect(container.querySelector('dialog')).not.toHaveAttribute('open'));
});
