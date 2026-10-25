import createClient from 'openapi-fetch';
import type { paths } from './schema.gen';

// Path, response and error types are derived from the OpenAPI-generated schema.
export const api = createClient<paths>({ baseUrl: new URL('/api', window.location.origin).href, credentials: 'same-origin' });

export async function getHealth() {
  const { data, error } = await api.GET('/healthz');
  if (error || !data) throw new Error('Service unavailable');
  return data;
}
