// maxcloud's web console. The server holds the login and reads the cluster
// as the logged-in user; the pages only ever talk to this server
// (docs/console.md).
export default defineNuxtConfig({
  compatibilityDate: '2026-07-01',
  devtools: { enabled: false },
  telemetry: false,
  css: ['~/assets/main.css'],
  app: {
    head: {
      htmlAttrs: { lang: 'en' },
      title: 'maxcloud console',
      meta: [
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'color-scheme', content: 'light dark' },
        { name: 'robots', content: 'noindex, nofollow' },
      ],
      link: [{ rel: 'icon', type: 'image/svg+xml', href: '/icon.svg' }],
    },
  },
  // Styles as files, not inline <style>: the Content-Security-Policy
  // (server/plugins/csp.ts) allows this site's stylesheets and nothing inline.
  features: { inlineStyles: false },
  experimental: {
    // No build manifest to fetch and no extracted payloads: the pages
    // aren't prerendered, and every answer is one user's.
    appManifest: false,
    payloadExtraction: false,
  },
  nitro: {
    preset: 'node-server',
  },
  typescript: { strict: true },
})
