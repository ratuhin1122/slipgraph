import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { expect, test, vi } from 'vitest';
import { MeetingPage, Meetings } from './Meetings';
import { AuthContext } from './auth-context';
import type { Session } from './api/client';
import type { components } from './api/schema.gen';
const id = '019a1111-1111-7111-8111-111111111111';
const segmentId = '019a2222-2222-7222-8222-222222222222';
const proposalId = '019a3333-3333-7333-8333-333333333333';
const session: Session = { id, user: { id, display_name: 'A', email: 'a@example.com' }, workspace_id: id, workspace_name: 'A', role: 'admin', expires_at: '2035-01-01T00:00:00Z' };
const meeting: components['schemas']['MeetingDetail'] = { id, project_id: id, title: 'Planning', media_type: 'audio/wav', size_bytes: 100, status: 'ready', duration_ms: 2000, created_at: '2030-01-01T00:00:00Z', extraction_status: 'complete', suggestions: [{ id: proposalId, kind: 'meeting_task', status: 'pending', title: 'Checklist', version: 1, payload_hash: 'a'.repeat(64), segment_id: segmentId, start_ms: 1000 }] };
const segment: components['schemas']['TranscriptSegment'] = { id: segmentId, ordinal: 0, start_ms: 1000, end_ms: 2000, speaker: 'Speaker A', text: '<img src="https://evil.example" onerror="alert(1)"> Action: make checklist.' };
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } });
function mount(view: 'page' | 'upload') { const cache = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } }); return render(<QueryClientProvider client={cache}><AuthContext value={{ session, pending: false, error: null, accept: vi.fn() }}><MemoryRouter initialEntries={[view === 'page' ? '/meetings/' + id + '?segment=' + segmentId : '/meetings']}><Routes><Route path="/meetings" element={<Meetings />} /><Route path="/meetings/:meetingId" element={view === 'page' ? <MeetingPage /> : <p>Uploaded meeting</p>} /></Routes></MemoryRouter></AuthContext></QueryClientProvider>); }
test('meeting uses private blob media, inert transcript, click-to-seek, and reviewed approval with CSRF', async () => {
 Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: vi.fn(() => 'blob:fixture') }); Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() });
 let approved = false; let searched = false;
 vi.mocked(fetch).mockImplementation(async input => { const req = input instanceof Request ? input : new Request(input); const url = new URL(req.url);
 if (url.pathname.endsWith('/csrf')) return json({ csrf_token: 'x'.repeat(43) });
 if (url.pathname.endsWith('/media')) return new Response(new Uint8Array([1, 2, 3]), { status: 200 });
 if (url.pathname.endsWith('/transcript')) { searched = url.searchParams.get('q') === 'checklist'; return json({ items: [segment], next_cursor: null }); }
 if (url.pathname.includes('/segments/')) return json(segment);
 if (url.pathname.endsWith('/approve')) { expect(req.headers.get('X-CSRF-Token')).toBe('x'.repeat(43)); expect(await req.json()).toEqual({ version: 1, payload_hash: 'a'.repeat(64) }); approved = true; return json({ status: 'approved' }); }
 if (url.pathname.includes('/proposals/')) return json({ id: proposalId, kind: 'meeting_task', status: 'pending', version: 1, payload_hash: 'a'.repeat(64), after: { task: { title: 'Checklist', description: 'Prepare release', start: '2030-01-01T00:00:00Z', due: '2030-01-02T00:00:00Z', estimate_minutes: 60 }, evidence: { quote: 'Action: make checklist.' } } });
 return json({ ...meeting, suggestions: meeting.suggestions.map(s => ({ ...s, status: approved ? 'approved' : 'pending' })) }); });
 const { container, unmount } = mount('page'); await screen.findByRole('heading', { name: 'Planning' }); await waitFor(() => expect(container.querySelector('audio')).toHaveAttribute('src', 'blob:fixture')); expect(container.querySelector('img,script,iframe')).toBeNull();
 const audio = container.querySelector('audio'); expect(audio).not.toBeNull(); if (!audio) throw new Error('no player'); Object.defineProperty(audio, 'readyState', { configurable: true, get: () => 1 });
 fireEvent.click(screen.getByRole('button', { name: 'Evidence at 0:01' })); expect(audio.currentTime).toBe(1);
 fireEvent.change(screen.getByLabelText('Search transcript'), { target: { value: 'checklist' } }); await waitFor(() => expect(searched).toBe(true));
 fireEvent.click(screen.getByRole('button', { name: 'Review and approve into tasks' })); await screen.findByRole('dialog'); fireEvent.click(await screen.findByRole('button', { name: 'Approve into tasks' })); await waitFor(() => expect(approved).toBe(true)); unmount(); expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:fixture');
});
test('upload uses multipart and CSRF through the generated client', async () => {
 let uploaded = false;
 vi.mocked(fetch).mockImplementation(async input => { const req = input instanceof Request ? input : new Request(input); const url = new URL(req.url);
 if (url.pathname.endsWith('/csrf')) return json({ csrf_token: 'x'.repeat(43) });
 if (url.pathname.endsWith('/settings')) return json({ enabled: true, mock: true, max_bytes: 25000000, max_minutes: 60, daily_minutes: 600 });
 if (url.pathname.endsWith('/projects')) return json([{ id, name: 'Launch' }]);
 if (req.method === 'POST') { expect(req.headers.get('Content-Type')).toContain('multipart/form-data'); expect(req.headers.get('X-CSRF-Token')).toBe('x'.repeat(43)); expect(req.body).not.toBeNull(); uploaded = true; return json(meeting); }
 return json([]); });
 mount('upload'); await waitFor(() => expect(screen.getByRole('button', { name: 'Upload recording' })).toBeEnabled()); fireEvent.change(screen.getByLabelText('Meeting title'), { target: { value: 'Weekly planning' } }); fireEvent.change(screen.getByLabelText('Project'), { target: { value: id } }); fireEvent.change(screen.getByLabelText('Audio or video'), { target: { files: [new File(['RIFF0000WAVE'], 'recording.wav', { type: 'audio/wav' })] } }); fireEvent.submit(screen.getByRole('button', { name: 'Upload recording' }).closest('form')!); expect(await screen.findByText('Uploaded meeting')).toBeInTheDocument(); expect(uploaded).toBe(true);
});
