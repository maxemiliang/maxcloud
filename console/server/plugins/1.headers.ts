// On every response, the build's files included: the headers that keep the
// pages and answers to themselves.
export default defineNitroPlugin((nitro) => {
  nitro.hooks.hook('request', (event) => {
    setResponseHeaders(event, {
      // Pages get their own policy with a nonce (csp.ts); everything else
      // (JSON, redirects, files) may load nothing at all.
      'Content-Security-Policy': 'default-src \'none\'; frame-ancestors \'none\'; base-uri \'none\'; form-action \'none\'',
      'X-Content-Type-Options': 'nosniff',
      'X-Frame-Options': 'DENY',
      // same-origin, not no-referrer: with no-referrer browsers send
      // "Origin: null" on the console's own form posts, which
      // server/middleware/0.guard.ts would refuse.
      'Referrer-Policy': 'same-origin',
      'Cross-Origin-Opener-Policy': 'same-origin',
      'Cross-Origin-Resource-Policy': 'same-origin',
      'Permissions-Policy': 'camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()',
      'X-Robots-Tag': 'noindex, nofollow',
      // The build's own files say otherwise for themselves (immutable).
      'Cache-Control': 'no-store',
    })
    if (runtime().config.secureCookies) {
      setResponseHeader(event, 'Strict-Transport-Security', 'max-age=31536000')
    }
  })
})
