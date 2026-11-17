// The login session: one sealed cookie holding the user's ID token and
// refresh token. The browser can't read or change it (httpOnly, sealed),
// and the tokens are only ever sent to the API server, the platform's apps
// API and the login provider.
import { createHash, timingSafeEqual } from 'node:crypto'
import { seal, unseal } from './seal'
import { OidcError, type TokenSet } from './oidc'

export interface Session {
  v: 1
  /** The username as the API server sees it, without its prefix. */
  user: string
  /** The ID token's subject: a refresh must come back for the same one. */
  sub: string
  idToken: string
  refreshToken: string
  /** When the ID token expires, in seconds since the epoch. */
  idExp: number
  /** When the user logged in (seconds). The session ends MAX_AGE later. */
  loginAt: number
}

/** What the login cookie holds between /auth/login and the callback. */
export interface LoginFlow {
  state: string
  nonce: string
  verifier: string
  next: string
  /** Seconds since the epoch. */
  exp: number
}

/** A login lasts as long as Authelia's refresh token: 12 hours, then log in again. */
export const MAX_AGE = 12 * 3600
/** A login has to finish within this (the browser at Authelia). */
export const LOGIN_FLOW_AGE = 600
/** Refresh this long before the ID token expires: it's sent on to slow calls. */
export const REFRESH_MARGIN = 300
/**
 * How long a refresh's result is kept for requests still carrying the old
 * cookie. A page's requests go out together, and the browser only has the
 * new cookie once the first answer is back.
 */
export const REFRESH_GRACE_MS = 60_000

export interface CookieNames { session: string, login: string }

/** __Host- cookies (https only, this host only, path /) wherever they can be. */
export function cookieNames(secure: boolean): CookieNames {
  return secure
    ? { session: '__Host-mc_session', login: '__Host-mc_login' }
    : { session: 'mc_session', login: 'mc_login' }
}

/** A cookie this large may be dropped by the browser (4096 bytes with its name and attributes). */
export const MAX_COOKIE_VALUE = 3800

export function sealSession(keys: Buffer[], s: Session): string {
  const v = seal(keys, 'session', s)
  if (v.length > MAX_COOKIE_VALUE) {
    throw new Error(`the session doesn't fit in a cookie (${v.length} bytes): the ID token is too large`)
  }
  return v
}

/** The session in a cookie value, or null: not ours, changed, or over. */
export function openSession(keys: Buffer[], value: string | undefined, nowMs: number): Session | null {
  const s = unseal<Session>(keys, 'session', value)
  if (!s || s.v !== 1 || typeof s.idToken !== 'string' || typeof s.user !== 'string' || typeof s.loginAt !== 'number') {
    return null
  }
  if (nowMs / 1000 >= s.loginAt + MAX_AGE) return null
  return s
}

export function sealLogin(keys: Buffer[], f: LoginFlow): string {
  return seal(keys, 'login', f)
}

export function openLogin(keys: Buffer[], value: string | undefined, nowMs: number): LoginFlow | null {
  const f = unseal<LoginFlow>(keys, 'login', value)
  if (!f || typeof f.state !== 'string' || typeof f.verifier !== 'string' || typeof f.nonce !== 'string') return null
  if (nowMs / 1000 >= f.exp) return null
  return f
}

/** Compares two secrets without telling how much of them matched. */
export function sameSecret(a: string, b: string): boolean {
  const x = createHash('sha256').update(a).digest()
  const y = createHash('sha256').update(b).digest()
  return timingSafeEqual(x, y)
}

/**
 * Where to go after logging in: a path on this site and nothing else. An
 * absolute URL, a scheme-relative one (//host), a backslash trick or the
 * login pages themselves all become "/".
 */
export function safeNext(next: unknown, origin: string): string {
  if (typeof next !== 'string' || next.length > 512) return '/'
  if (!next.startsWith('/') || next.startsWith('//') || /[\\\u0000-\u001f\u007f]/.test(next)) return '/'
  let u: URL
  try {
    u = new URL(next, origin)
  } catch {
    return '/'
  }
  if (u.origin !== origin) return '/'
  if (u.pathname === '/login' || u.pathname.startsWith('/auth/') || u.pathname.startsWith('/api/') || u.pathname === '/callback') {
    return '/'
  }
  return u.pathname + u.search
}

export type Fresh
  = | { kind: 'ok', session: Session, changed: boolean }
    /** The login is over (expired, revoked, the refresh token used up): log in again. */
    | { kind: 'over' }
    /** The provider can't be asked and the ID token has run out: try again shortly. */
    | { kind: 'unavailable' }

interface Flight {
  result: Promise<Session | null>
  /** When the refresh finished (ms); 0 while it runs. */
  doneAt: number
}

/**
 * Keeps a session's ID token fresh.
 *
 * Authelia's refresh tokens rotate: each works once, and using one twice
 * can end the whole login. A page sends several requests at once, all with
 * the same cookie, so without care two of them would refresh with the same
 * token. Here the first starts the refresh and the others wait for it and
 * get the same new tokens (a single flight per refresh token). The result
 * stays for a minute, for requests that left the browser before the new
 * cookie arrived. Each of them is answered with the new cookie.
 *
 * This lives in one process: the console runs as a single instance
 * (max-scale 1). A second instance would only mean that such a late request
 * has to log in again.
 */
export class Refresher {
  private flights = new Map<string, Flight>()

  constructor(
    private refresh: (refreshToken: string) => Promise<TokenSet>,
    private now: () => number = Date.now,
    private graceMs: number = REFRESH_GRACE_MS,
  ) {}

  /** The session with an ID token good for a while yet. */
  async fresh(s: Session): Promise<Fresh> {
    const nowS = this.now() / 1000
    if (nowS >= s.loginAt + MAX_AGE) return { kind: 'over' }
    if (s.idExp - nowS > REFRESH_MARGIN) return { kind: 'ok', session: s, changed: false }
    if (!s.refreshToken) {
      // Nothing to refresh with: the ID token's own lifetime is the login's.
      return s.idExp > nowS ? { kind: 'ok', session: s, changed: false } : { kind: 'over' }
    }
    try {
      const next = await this.flight(s)
      return next ? { kind: 'ok', session: next, changed: true } : { kind: 'over' }
    } catch {
      // The provider couldn't be asked. The token we have may still do.
      return s.idExp - nowS > 5 ? { kind: 'ok', session: s, changed: false } : { kind: 'unavailable' }
    }
  }

  private flight(s: Session): Promise<Session | null> {
    this.sweep()
    const key = createHash('sha256').update(s.refreshToken).digest('base64')
    const running = this.flights.get(key)
    if (running) return running.result

    const flight: Flight = { doneAt: 0, result: Promise.resolve(null) }
    flight.result = this.refresh(s.refreshToken).then(
      (t): Session | null => {
        flight.doneAt = this.now()
        // A refresh never changes who is logged in.
        if (t.claims.sub !== s.sub) return null
        return {
          ...s,
          idToken: t.idToken,
          refreshToken: t.refreshToken || s.refreshToken,
          idExp: t.claims.exp,
        }
      },
      (e) => {
        if (e instanceof OidcError && e.kind === 'unavailable') {
          // Not an answer: let the next request try again.
          this.flights.delete(key)
          throw e
        }
        // Refused, or an answer that can't be trusted: the login is over,
        // for the requests behind this one too.
        flight.doneAt = this.now()
        return null
      },
    )
    this.flights.set(key, flight)
    return flight.result
  }

  /** Drops results older than the grace period. */
  private sweep() {
    const cutoff = this.now() - this.graceMs
    for (const [k, f] of this.flights) {
      if (f.doneAt && f.doneAt < cutoff) this.flights.delete(k)
    }
    // A bound, should something go very wrong: the oldest go first.
    while (this.flights.size > 2000) {
      this.flights.delete(this.flights.keys().next().value!)
    }
  }

  /** For tests: how many refreshes are remembered. */
  get size() { return this.flights.size }
}
