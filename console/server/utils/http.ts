// What the routes share: the session cookie, the reader for the logged-in
// user, and turning what went wrong into an answer that says so without
// saying too much.
import type { H3Event } from 'h3'
import { NotAvailable } from '../lib/apps'
import { AppsApiUnauthorized } from '../lib/appsapi'
import { isAppName, KubeError, KubeReader, KubeUnauthorized } from '../lib/kube'
import { MAX_AGE, sealSession, type Session } from '../lib/session'

/** httpOnly: no script reads it. SameSite=Lax: only sent with requests this site makes, and with links to it. */
function cookieAttrs() {
  const { config } = runtime()
  return { httpOnly: true, secure: config.secureCookies, sameSite: 'lax' as const, path: '/' }
}

export function setSessionCookie(event: H3Event, s: Session) {
  const rt = runtime()
  const left = Math.max(1, Math.floor(s.loginAt + MAX_AGE - Date.now() / 1000))
  setCookie(event, rt.cookies.session, sealSession(rt.config.sessionKeys, s), { ...cookieAttrs(), maxAge: left })
}

export function clearSessionCookie(event: H3Event) {
  deleteCookie(event, runtime().cookies.session, cookieAttrs())
}

export function setLoginCookie(event: H3Event, value: string, maxAge: number) {
  setCookie(event, runtime().cookies.login, value, { ...cookieAttrs(), maxAge })
}

export function clearLoginCookie(event: H3Event) {
  deleteCookie(event, runtime().cookies.login, cookieAttrs())
}

/** The session server/middleware/1.session.ts found, already refreshed. */
export function requireSession(event: H3Event): Session {
  const s = event.context.session as Session | null | undefined
  if (!s) throw createError({ statusCode: 401, statusMessage: 'Unauthorized', message: 'Not logged in.' })
  return s
}

/** Reads the cluster as the logged-in user: their token, their rights. */
export function kubeFor(event: H3Event): KubeReader {
  return new KubeReader(runtime().transport, requireSession(event).idToken)
}

/** Answers for an error from the cluster or the platform. Never throws the original: it may quote upstream text. */
export function fail(event: H3Event, e: unknown): never {
  if (e instanceof KubeUnauthorized || e instanceof AppsApiUnauthorized) {
    // The token the session holds isn't accepted any more: start over.
    clearSessionCookie(event)
    throw createError({ statusCode: 401, statusMessage: 'Unauthorized', message: 'The cluster didn\'t accept your login. Log in again.' })
  }
  if (e instanceof NotAvailable) {
    throw createError({ statusCode: e.status, statusMessage: e.status === 403 ? 'Forbidden' : 'Not Found', message: e.message })
  }
  if (e instanceof KubeError) {
    console.error(`console: ${event.path.split('?')[0]}: ${e.message}`)
    throw createError({ statusCode: 502, statusMessage: 'Bad Gateway', message: 'The cluster couldn\'t be read right now. Try again in a moment.' })
  }
  if (isError(e)) throw e
  console.error(`console: ${event.path.split('?')[0]}: ${(e as Error)?.message ?? 'unknown error'}`)
  throw createError({ statusCode: 500, statusMessage: 'Internal Server Error', message: 'Something went wrong in the console.' })
}

/** The app in the route, if it could be an app's name; 404 otherwise. */
export function appParam(event: H3Event): string {
  const app = getRouterParam(event, 'app')
  if (!isAppName(app)) throw createError({ statusCode: 404, statusMessage: 'Not Found', message: 'No such app.' })
  return app
}
