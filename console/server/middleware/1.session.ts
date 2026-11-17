// Finds the login session, keeps its ID token fresh, and sends anyone
// without one to the login page before a page or the API does any work.
import { openSession, safeNext } from '../lib/session'

/** Served to anyone: the build's files and the health check. */
const isPublicFile = (path: string) =>
  path.startsWith('/_nuxt/') || path === '/healthz' || path === '/icon.svg' || path === '/favicon.ico' || path === '/robots.txt'

export default defineEventHandler(async (event) => {
  const path = event.path.split('?')[0]!
  if (isPublicFile(path)) return
  const rt = runtime()
  const raw = getCookie(event, rt.cookies.session)
  let session = openSession(rt.config.sessionKeys, raw, Date.now())
  let providerDown = false
  if (session) {
    const fresh = await rt.refresher.fresh(session)
    if (fresh.kind === 'ok') {
      session = fresh.session
      // Every request that came with the old cookie leaves with the new one.
      if (fresh.changed) setSessionCookie(event, session)
    } else {
      session = null
      if (fresh.kind === 'over') clearSessionCookie(event)
      else providerDown = true
    }
  } else if (raw) {
    // Not ours, or older than a login lasts.
    clearSessionCookie(event)
  }
  event.context.session = session
  // For the login page: whether to offer the mock's made-up users.
  event.context.mock = rt.config.mock

  // The login routes decide for themselves; Nuxt's error page renders for anyone.
  if (path.startsWith('/auth/') || path === '/callback' || path.startsWith('/__nuxt')) return
  if (session) {
    if (path === '/login') return sendRedirect(event, '/', 302)
    return
  }
  if (providerDown) {
    throw createError({ statusCode: 503, statusMessage: 'Service Unavailable', message: 'The login provider can\'t be reached to renew your login. Try again in a moment.' })
  }
  if (path.startsWith('/api/')) {
    throw createError({ statusCode: 401, statusMessage: 'Unauthorized', message: 'Not logged in.' })
  }
  if (path === '/login') return
  const next = safeNext(event.path, rt.config.origin)
  return sendRedirect(event, next === '/' ? '/login' : `/login?next=${encodeURIComponent(next)}`, 302)
})
