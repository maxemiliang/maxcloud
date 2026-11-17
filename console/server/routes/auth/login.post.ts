// Starts a login: remembers, in a sealed cookie only this browser holds,
// the state, the nonce and the PKCE verifier, then sends the browser to
// Authelia. The callback only finishes a login this browser started.
import { MOCK_USERS, mockToken } from '../../lib/mock'
import { pkceChallenge, randomToken } from '../../lib/oidc'
import { LOGIN_FLOW_AGE, MAX_AGE, safeNext, sealLogin } from '../../lib/session'

export default defineEventHandler(async (event) => {
  const rt = runtime()
  const body = await readBody<Record<string, unknown> | undefined>(event).catch(() => undefined)
  const next = safeNext(body?.next, rt.config.origin)
  const now = Math.floor(Date.now() / 1000)

  if (rt.config.mock) {
    const user = typeof body?.user === 'string' && Object.hasOwn(MOCK_USERS, body.user) ? body.user : 'alice'
    setSessionCookie(event, { v: 1, user, sub: user, idToken: mockToken(user), refreshToken: '', idExp: now + MAX_AGE, loginAt: now })
    return sendRedirect(event, next, 303)
  }

  const flow = { state: randomToken(), nonce: randomToken(), verifier: randomToken(48), next, exp: now + LOGIN_FLOW_AGE }
  let url: string
  try {
    url = await rt.oidc.authorizationUrl({ state: flow.state, nonce: flow.nonce, challenge: pkceChallenge(flow.verifier) })
  } catch (e) {
    console.error(`console: login: ${(e as Error).message}`)
    return sendRedirect(event, '/login?error=provider', 303)
  }
  setLoginCookie(event, sealLogin(rt.config.sessionKeys, flow), LOGIN_FLOW_AGE)
  return sendRedirect(event, url, 303)
})
