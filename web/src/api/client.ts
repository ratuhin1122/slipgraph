import createClient from 'openapi-fetch';
import type { components, paths } from './schema.gen';

export type Session = components['schemas']['Session'];
export type Project = components['schemas']['Project'];
export type Task = components['schemas']['Task'];
export type Member = components['schemas']['Member'];
export type Board = components['schemas']['Board'];
export type RiskEnvelope = components['schemas']['RiskEnvelope'];
export type TaskImpact = components['schemas']['TaskImpact'];
export type TaskInput = components['schemas']['TaskInput'];
export type Role = components['schemas']['Role'];
export class APIError extends Error { constructor(public status: number, message: string) { super(message); } }
function message(value: unknown): string {
  if (typeof value === 'object' && value !== null && 'error' in value && typeof value.error === 'string') return value.error;
  return 'Request failed';
}
export function dataOrThrow<T>(result: { data?: T | undefined; error?: unknown; response: Response }): T {
  if (result.error !== undefined || result.data === undefined) throw new APIError(result.response.status, result.response.status === 409 ? 'This record changed or the dependency would create a cycle. Reload before retrying.' : message(result.error));
  return result.data;
}
export const api = createClient<paths>({ baseUrl: new URL('/api', window.location.origin).href, credentials: 'same-origin' });
api.use({ async onRequest({ request }) {
  if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method)) {
    const response = await fetch(new URL('/api/auth/csrf', window.location.origin), { credentials: 'same-origin', cache: 'no-store' });
    const value: unknown = await response.json();
    if (!response.ok || typeof value !== 'object' || value === null || !('csrf_token' in value) || typeof value.csrf_token !== 'string') throw new APIError(403, 'Could not establish a secure request');
    request.headers.set('X-CSRF-Token', value.csrf_token);
  }
  return request;
} });
export async function getHealth() { return dataOrThrow(await api.GET('/healthz')); }
export async function getSession(): Promise<Session | null> {
  const result = await api.GET('/auth/session');
  if (result.response.status !== 401) return dataOrThrow(result);
  const refreshed = await api.POST('/auth/refresh');
  if (refreshed.response.status === 401 || refreshed.response.status === 403) return null;
  return dataOrThrow(refreshed);
}
export function taskInput(task: Task): TaskInput { return { title: task.title, description: task.description, status: task.status, owner_id: task.owner_id, start: task.start, due: task.due, estimate_minutes: task.estimate_minutes, milestone: task.milestone }; }
