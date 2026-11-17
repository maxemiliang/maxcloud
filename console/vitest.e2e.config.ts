import { defineConfig } from 'vitest/config'

// The built server (npm run build first) against a made-up Authelia, API
// server and apps API on this machine: test/e2e.
export default defineConfig({
  test: { include: ['test/e2e/*.test.ts'], environment: 'node', testTimeout: 30_000, hookTimeout: 30_000, fileParallelism: false },
})
