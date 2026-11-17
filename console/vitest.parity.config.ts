import { defineConfig } from 'vitest/config'

// The console against mcloud itself (needs Go): test/parity.
export default defineConfig({
  test: { include: ['test/parity/*.test.ts'], environment: 'node', testTimeout: 60_000, hookTimeout: 300_000 },
})
