// Logs out of the console: the cookie goes, and the refresh token is handed
// back to Authelia so it can't be used again. Authelia's own session (and
// mcloud's login) aren't touched.
import type { Session } from '../../lib/session'

export default defineEventHandler((event) => {
  const rt = runtime()
  const session = event.context.session as Session | null | undefined
  clearSessionCookie(event)
  clearLoginCookie(event)
  if (session?.refreshToken && !rt.config.mock) {
    // Not waited for: logging out works with Authelia down too.
    void rt.oidc.revoke(session.refreshToken)
  }
  return sendRedirect(event, '/login?out=1', 303)
})
