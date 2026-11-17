// Finishes a login: Authelia sends the browser back here with a code.
import type { H3Event } from 'h3'
import { KubeReader, KubeUnauthorized } from '../lib/kube'
import { openLogin, sameSecret } from '../lib/session'

/**
 * Why a login didn't finish, for /login?error=. Fixed words: nothing the
 * request carried is echoed onto the page.
 */
export type LoginError = 'expired' | 'state' | 'denied' | 'provider' | 'cluster' | 'not_allowed'

export async function loginCallback(event: H3Event) {
  const rt = runtime()
  if (rt.config.mock) return sendRedirect(event, '/', 303)
  const failed = (why: LoginError) => sendRedirect(event, `/login?error=${why}`, 303)
  const q = getQuery(event)

  // The cookie /auth/login set in this browser. One use: it's gone whatever
  // happens next.
  const flow = openLogin(rt.config.sessionKeys, getCookie(event, rt.cookies.login), Date.now())
  clearLoginCookie(event)
  if (!flow) return failed('expired')
  // The state ties this callback to that cookie: a link someone else made
  // (with their own code) carries a state this browser never had.
  if (typeof q.state !== 'string' || !sameSecret(q.state, flow.state)) return failed('state')
  if (q.error) return failed(q.error === 'access_denied' ? 'denied' : 'provider')
  if (typeof q.code !== 'string' || !q.code || q.code.length > 4096) return failed('provider')

  let tokens
  try {
    // With the PKCE verifier from the cookie, and checked against its nonce.
    tokens = await rt.oidc.exchangeCode(q.code, flow.verifier, flow.nonce)
  } catch (e) {
    console.error(`console: login: ${(e as Error).message}`)
    return failed('provider')
  }

  // Who the cluster says this is. The console never decides that itself.
  let who
  try {
    who = await new KubeReader(rt.transport, tokens.idToken).whoami()
  } catch (e) {
    if (e instanceof KubeUnauthorized) return failed('not_allowed')
    console.error(`console: login: ${(e as Error).message}`)
    return failed('cluster')
  }
  // People only: their usernames carry the provider's prefix.
  if (!rt.config.userPrefix || !who.username.startsWith(rt.config.userPrefix)) return failed('not_allowed')
  if (!tokens.refreshToken) {
    console.warn('console: login: the provider gave no refresh token; the session ends with the ID token')
  }

  setSessionCookie(event, {
    v: 1,
    user: who.username.slice(rt.config.userPrefix.length),
    sub: tokens.claims.sub,
    idToken: tokens.idToken,
    refreshToken: tokens.refreshToken,
    idExp: tokens.claims.exp,
    loginAt: Math.floor(Date.now() / 1000),
  })
  return sendRedirect(event, flow.next, 303)
}
