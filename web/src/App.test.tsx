import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import App from './App';

function renderApp(path = '/') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><App /></MemoryRouter></QueryClientProvider>);
}
test('shows readiness from the typed API client', async () => {
  vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ status: 'ok' }), { headers: { 'Content-Type': 'application/json' } }));
  renderApp();
  expect(await screen.findByText('All services ready')).toBeInTheDocument();
  const request = vi.mocked(fetch).mock.calls[0]?.[0];
  if (!(request instanceof Request)) throw new Error('Expected a typed client request');
  expect(new URL(request.url).pathname).toBe('/api/healthz');
  expect(request.method).toBe('GET');
});
test('shows a safe error when API is unavailable', async () => {
  vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ status: 'unavailable' }), { status: 503, headers: { 'Content-Type': 'application/json' } }));
  renderApp();
  expect(await screen.findByText('Services unavailable')).toBeInTheDocument();
});
test('handles unknown paths', () => { renderApp('/missing'); expect(screen.getByRole('heading', { name: 'Page not found' })).toBeInTheDocument(); });
