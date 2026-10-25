import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: { proxy: { '/api': { target: 'http://localhost:8080', rewrite: (path) => path.replace(/^\/api/, '') } } },
  test: { pool: 'threads', maxWorkers: 1, environment: 'jsdom', setupFiles: ['./src/test/setup.ts'], restoreMocks: true },
});
