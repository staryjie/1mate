import { defineConfig } from 'vitest/config'

// Kept separate from vite.config.ts so the app build uses the installed Vite
// version while tests resolve their own copy through Vitest.
export default defineConfig({
  esbuild: { jsx: 'automatic' },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test/setup.ts',
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
