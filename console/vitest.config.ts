import { defineConfig } from 'vitest/config'

// The server's libraries are plain modules (server/lib), tested without
// Nuxt or a cluster.
export default defineConfig({
  test: { include: ['test/*.test.ts'], environment: 'node' },
})
