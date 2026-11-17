// Every page gets a Content-Security-Policy of its own: scripts only from
// this site and only with this response's nonce, styles and images only
// from this site, nothing from anywhere else, no frames. Nuxt's own inline
// scripts (its config, the import map) get the nonce here, so no
// 'unsafe-inline' is needed.
import { randomBytes } from 'node:crypto'

export default defineNitroPlugin((nitro) => {
  nitro.hooks.hook('render:html', (html, { event }) => {
    // The dev server's hot reloading needs inline scripts and a websocket.
    if (import.meta.dev) return
    const { config } = runtime()
    const nonce = randomBytes(18).toString('base64')
    // Raw "<script" in these chunks is only ever a real tag: Vue escapes
    // text, and the payload's JSON escapes "<".
    const sign = (chunks: string[]) => chunks.map(c => c.replace(/<script(?=[\s>])/g, `<script nonce="${nonce}"`))
    html.head = sign(html.head)
    html.bodyPrepend = sign(html.bodyPrepend)
    html.body = sign(html.body)
    html.bodyAppend = sign(html.bodyAppend)
    setResponseHeader(event, 'Content-Security-Policy', [
      'default-src \'none\'',
      `script-src 'self' 'nonce-${nonce}'`,
      'style-src \'self\'',
      'img-src \'self\' data:',
      'font-src \'self\'',
      'connect-src \'self\'',
      // Logging in posts to this site, which redirects to the login
      // provider; Chrome checks the redirect against form-action too.
      `form-action 'self' ${config.mock ? '' : new URL(config.oidc.issuer).origin}`.trim(),
      'frame-ancestors \'none\'',
      'base-uri \'none\'',
      'manifest-src \'self\'',
    ].join('; '))
  })
})
