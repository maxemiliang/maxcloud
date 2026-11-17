// OpenID Connect against Authelia, done by the server: the authorization
// code flow with PKCE for a public client (no client secret). The ID token
// is what the Kubernetes API server accepts as a login, so it never leaves
// the server.
//
// Nothing here logs or throws a token: errors carry the provider's error
// code and description only.
import { createHash, createPublicKey, createVerify, randomBytes, type KeyObject } from 'node:crypto'

export type Fetch = typeof fetch

export interface OidcOptions {
  issuer: string
  clientId: string
  redirectUri: string
  fetch?: Fetch
  now?: () => number
}

export interface TokenSet {
  idToken: string
  /** Empty when the provider gave none (no offline_access). */
  refreshToken: string
  claims: IdClaims
}

export interface IdClaims {
  iss: string
  sub: string
  aud: string | string[]
  exp: number
  iat?: number
  nonce?: string
  azp?: string
  preferred_username?: string
}

/**
 * Why a call to the provider failed:
 * - denied: the provider said no (invalid_grant: the code or refresh token
 *   is used up, expired or revoked). The login is over.
 * - unavailable: it couldn't be asked (network, 5xx). Worth trying again.
 * - protocol: it answered something that can't be trusted (a bad token).
 */
export class OidcError extends Error {
  constructor(public kind: 'denied' | 'unavailable' | 'protocol', message: string) {
    super(message)
  }
}

interface Discovery {
  authorization_endpoint: string
  token_endpoint: string
  jwks_uri: string
  revocation_endpoint?: string
}

interface Jwk {
  kid?: string
  kty: string
  use?: string
  alg?: string
  [k: string]: unknown
}

export const SCOPES = 'openid profile email groups offline_access'

const b64url = (b: Buffer) => b.toString('base64url')

/** A random URL-safe string: the state, the nonce, the PKCE verifier. */
export function randomToken(bytes = 32): string {
  return b64url(randomBytes(bytes))
}

/** PKCE's S256 challenge for a verifier. */
export function pkceChallenge(verifier: string): string {
  return b64url(createHash('sha256').update(verifier, 'ascii').digest())
}

/** A JWT's payload, unverified. Null if it isn't a JWT. */
export function decodeJwtPayload(jwt: string): Record<string, unknown> | null {
  const parts = jwt.split('.')
  if (parts.length !== 3) return null
  try {
    const v = JSON.parse(Buffer.from(parts[1]!, 'base64url').toString('utf8'))
    return v && typeof v === 'object' && !Array.isArray(v) ? v : null
  } catch {
    return null
  }
}

export class Oidc {
  private fetch: Fetch
  private now: () => number
  private discovery?: { doc: Discovery, at: number }
  private keys = new Map<string, KeyObject>()
  private keysAt = 0

  constructor(private opts: OidcOptions) {
    this.fetch = opts.fetch ?? fetch
    this.now = opts.now ?? Date.now
  }

  private async json(url: string, init?: RequestInit): Promise<{ status: number, body: any }> {
    let resp: Response
    try {
      resp = await this.fetch(url, {
        ...init,
        redirect: 'error',
        signal: AbortSignal.timeout(10_000),
        headers: { accept: 'application/json', ...init?.headers },
      })
    } catch (e) {
      throw new OidcError('unavailable', `the login provider can't be reached (${(e as Error).name})`)
    }
    const text = await resp.text().catch(() => '')
    let body: any = null
    try {
      body = JSON.parse(text)
    } catch {
      // Not JSON: an error page from a proxy.
    }
    return { status: resp.status, body }
  }

  /** The provider's endpoints, from its discovery document (cached 10 minutes). */
  private async discover(): Promise<Discovery> {
    const cached = this.discovery
    if (cached && this.now() - cached.at < 600_000) return cached.doc
    let doc: any
    try {
      const { status, body } = await this.json(`${this.opts.issuer}/.well-known/openid-configuration`)
      if (status !== 200 || !body) throw new OidcError('unavailable', `the login provider's discovery document: HTTP ${status}`)
      doc = body
    } catch (e) {
      // A stale copy beats no login while the provider restarts.
      if (cached) return cached.doc
      throw e
    }
    if (doc.issuer !== this.opts.issuer) {
      throw new OidcError('protocol', 'the login provider\'s discovery document names another issuer')
    }
    const origin = new URL(this.opts.issuer).origin
    for (const k of ['authorization_endpoint', 'token_endpoint', 'jwks_uri'] as const) {
      // The endpoints live with the issuer: a document that sends the code
      // or the tokens anywhere else isn't followed.
      if (typeof doc[k] !== 'string' || new URL(doc[k]).origin !== origin) {
        throw new OidcError('protocol', `the login provider's discovery document: ${k} isn't on the issuer's origin`)
      }
    }
    const out: Discovery = {
      authorization_endpoint: doc.authorization_endpoint,
      token_endpoint: doc.token_endpoint,
      jwks_uri: doc.jwks_uri,
    }
    if (typeof doc.revocation_endpoint === 'string' && new URL(doc.revocation_endpoint).origin === origin) {
      out.revocation_endpoint = doc.revocation_endpoint
    }
    this.discovery = { doc: out, at: this.now() }
    return out
  }

  /** Where to send the browser to log in. */
  async authorizationUrl(p: { state: string, nonce: string, challenge: string }): Promise<string> {
    const d = await this.discover()
    const u = new URL(d.authorization_endpoint)
    u.search = new URLSearchParams({
      response_type: 'code',
      client_id: this.opts.clientId,
      redirect_uri: this.opts.redirectUri,
      scope: SCOPES,
      state: p.state,
      nonce: p.nonce,
      code_challenge: p.challenge,
      code_challenge_method: 'S256',
    }).toString()
    return u.href
  }

  private async token(form: Record<string, string>, nonce?: string): Promise<TokenSet> {
    const d = await this.discover()
    const { status, body } = await this.json(d.token_endpoint, {
      method: 'POST',
      headers: { 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ ...form, client_id: this.opts.clientId }).toString(),
    })
    if (status >= 500 || status === 429) {
      throw new OidcError('unavailable', `the login provider answered HTTP ${status}`)
    }
    if (status !== 200 || !body) {
      const code = typeof body?.error === 'string' ? body.error : `HTTP ${status}`
      const desc = typeof body?.error_description === 'string' ? `: ${body.error_description.slice(0, 200)}` : ''
      throw new OidcError('denied', `the login provider refused (${code}${desc})`)
    }
    if (typeof body.id_token !== 'string') {
      throw new OidcError('protocol', 'the login provider returned no ID token (is the openid scope allowed for the client?)')
    }
    const claims = await this.verifyIdToken(body.id_token, nonce)
    return {
      idToken: body.id_token,
      refreshToken: typeof body.refresh_token === 'string' ? body.refresh_token : '',
      claims,
    }
  }

  /** Trades the callback's code (and the PKCE verifier) for tokens. */
  exchangeCode(code: string, verifier: string, nonce: string): Promise<TokenSet> {
    return this.token({
      grant_type: 'authorization_code',
      code,
      redirect_uri: this.opts.redirectUri,
      code_verifier: verifier,
    }, nonce)
  }

  /** A new ID token from a refresh token. The provider rotates it: use the one returned. */
  refresh(refreshToken: string): Promise<TokenSet> {
    return this.token({ grant_type: 'refresh_token', refresh_token: refreshToken })
  }

  /** Tells the provider a refresh token is done with (log out). Best effort. */
  async revoke(refreshToken: string): Promise<void> {
    try {
      const d = await this.discover()
      if (!d.revocation_endpoint) return
      await this.json(d.revocation_endpoint, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          token: refreshToken,
          token_type_hint: 'refresh_token',
          client_id: this.opts.clientId,
        }).toString(),
      })
    } catch {
      // It expires by itself.
    }
  }

  private async key(kid: string | undefined): Promise<KeyObject | null> {
    const find = () => (kid ? this.keys.get(kid) : this.keys.size === 1 ? [...this.keys.values()][0] : undefined)
    let k = find()
    // An unknown key ID: the provider may have rotated. Ask again, at most
    // every 30 seconds, so made-up tokens can't make us hammer it.
    if (!k && this.now() - this.keysAt > 30_000) {
      const d = await this.discover()
      const { status, body } = await this.json(d.jwks_uri)
      if (status !== 200 || !Array.isArray(body?.keys)) {
        throw new OidcError('unavailable', `the login provider's keys: HTTP ${status}`)
      }
      const keys = new Map<string, KeyObject>()
      for (const jwk of body.keys as Jwk[]) {
        if (jwk.kty !== 'RSA' || (jwk.use && jwk.use !== 'sig')) continue
        try {
          keys.set(jwk.kid ?? `#${keys.size}`, createPublicKey({ key: jwk as any, format: 'jwk' }))
        } catch {
          // Not a key we can read.
        }
      }
      this.keys = keys
      this.keysAt = this.now()
      k = find()
    }
    return k ?? null
  }

  /**
   * Checks an ID token: signed by the provider (RS256), issued by it, for
   * this client, not expired, and carrying the nonce the login started with.
   */
  async verifyIdToken(jwt: string, nonce?: string): Promise<IdClaims> {
    const bad = (why: string) => new OidcError('protocol', `the ID token ${why}`)
    const parts = jwt.split('.')
    if (parts.length !== 3) throw bad('isn\'t a JWT')
    let header: any
    try {
      header = JSON.parse(Buffer.from(parts[0]!, 'base64url').toString('utf8'))
    } catch {
      throw bad('has no readable header')
    }
    // One algorithm, named here and not taken from the token.
    if (header?.alg !== 'RS256') throw bad('isn\'t signed with RS256')
    const key = await this.key(typeof header.kid === 'string' ? header.kid : undefined)
    if (!key) throw bad('is signed with a key the provider doesn\'t publish')
    const ok = createVerify('RSA-SHA256')
      .update(`${parts[0]}.${parts[1]}`)
      .verify(key, Buffer.from(parts[2]!, 'base64url'))
    if (!ok) throw bad('has a bad signature')

    const c = decodeJwtPayload(jwt) as Partial<IdClaims> | null
    if (!c) throw bad('has no readable claims')
    if (c.iss !== this.opts.issuer) throw bad('is from another issuer')
    const aud = Array.isArray(c.aud) ? c.aud : [c.aud]
    if (!aud.includes(this.opts.clientId)) throw bad('is for another client')
    if (aud.length > 1 && c.azp !== this.opts.clientId) throw bad('is for another client')
    if (typeof c.exp !== 'number' || c.exp * 1000 <= this.now()) throw bad('has expired')
    if (typeof c.iat === 'number' && c.iat * 1000 > this.now() + 60_000) throw bad('is from the future')
    if (typeof c.sub !== 'string' || !c.sub) throw bad('names nobody')
    if (nonce !== undefined && c.nonce !== nonce) throw bad('doesn\'t belong to this login (nonce)')
    return c as IdClaims
  }
}
