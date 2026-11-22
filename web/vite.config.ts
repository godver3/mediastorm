import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { tanstackRouter } from '@tanstack/router-plugin/vite';
import { DEV_BASE } from './base.config';

const BACKEND = process.env.WEB_BACKEND ?? 'http://127.0.0.1:7777';

export default defineConfig(({ command }) => ({
  // Relative in builds (see base.config.ts / index.html), absolute for the dev server.
  base: command === 'build' ? './' : DEV_BASE,
  plugins: [tanstackRouter({ target: 'react', autoCodeSplitting: true }), react(), tailwindcss()],
  resolve: {
    alias: { '@': new URL('./src', import.meta.url).pathname },
  },
  server: {
    port: 5180,
    proxy: {
      '/api': { target: BACKEND, changeOrigin: true },
      '/watch/playback.html': { target: BACKEND, changeOrigin: true },
      '/favicon.ico': { target: BACKEND, changeOrigin: true },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
  },
}));
