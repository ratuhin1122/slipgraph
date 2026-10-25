import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';

// Install before modules create their OpenAPI client (which captures fetch).
vi.stubGlobal('fetch', vi.fn());
afterEach(() => { cleanup(); vi.mocked(fetch).mockReset(); });
