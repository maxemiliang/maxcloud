import { createHmac, generateKeyPairSync } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { decodeJwtPayload, Oidc, OidcError, pkceChallenge, randomToken, SCOPES } from '../server/lib/oidc'
import { fakeProvider, s256 } from './helpers/provider'

const ISSUER = 'https://auth.example.com'
const REDIRECT = 'https://console.example.com/auth/callback'
const NOW = 1_800_000_000_000
const nowS = NOW / 1000

function setup(over: { discovery?: Record<string, unknown>, token?: (form: URLSearchParams) => { status: number, body: unknown } } = {}) {
  const p = fakeProvider(ISSUER)
  const requests: { url: string, init?: RequestInit }[] = []
  const claims = (extra: Record<string, unknown> = {}) => ({
    iss: ISSUER, sub: 'sub-alice', aud: ['kubernetes'], exp: nowS + 3600, iat: nowS, nonce: 'the-nonce', ...extra,
  })
  const fetch = (async (url: string, init?: RequestInit) => {
    requests.push({ url, init })
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
    if (url === `${ISSUER}/.well-known/openid-configuration`) return json(200, { ...p.discovery(), ...over.discovery })
    if (url === `${ISSUER}/jwks.json`) return json(200, p.jwks())
    if (url === `${ISSUER}/api/oidc/token`) {
      const form = new URLSearchParams(String(init?.body))
      const out = over.token?.(form) ?? { status: 200, body: { id_token: p.idToken(claims()), refresh_token: 'rt-new', access_token: 'at' } }
      return json(out.status, out.body)
    }
    if (url === `${ISSUER}/api/oidc/revocation`) return json(200, {})
    return json(404, {})
  }) as unknown as typeof fetch
  const oidc = new Oidc({ issuer: ISSUER, clientId: 'kubernetes', redirectUri: REDIRECT, fetch, now: () => NOW })
  return { p, oidc, requests, claims }
}

describe('starting a login', () => {
  it('sends the browser to the provider with PKCE, a state and a nonce', async () => {
    const { oidc } = setup()
    const verifier = randomToken(48)
    const url = new URL(await oidc.authorizationUrl({ state: 'st', nonce: 'no', challenge: pkceChallenge(verifier) }))
    expect(url.origin + url.pathname).toBe(`${ISSUER}/api/oidc/authorization`)
    expect(Object.fromEntries(url.searchParams)).toEqual({
      response_type: 'code',
      client_id: 'kubernetes',
      redirect_uri: REDIRECT,
      scope: SCOPES,
      state: 'st',
      nonce: 'no',
      code_challenge: s256(verifier),
      code_challenge_method: 'S256',
    })
    // The verifier itself stays here.
    expect(url.href).not.toContain(verifier)
  })

  it('makes tokens nobody can guess', () => {
    const seen = new Set(Array.from({ length: 200 }, () => randomToken()))
    expect(seen.size).toBe(200)
    expect(randomToken()).toMatch(/^[A-Za-z0-9_-]{43}$/)
    // RFC 7636: a verifier is 43 to 128 characters.
    expect(randomToken(48)).toHaveLength(64)
  })

  it('doesn\'t follow a discovery document that names another issuer or sends tokens elsewhere', async () => {
    await expect(setup({ discovery: { issuer: 'https://evil.example' } }).oidc.authorizationUrl({ state: 's', nonce: 'n', challenge: 'c' }))
      .rejects.toMatchObject({ kind: 'protocol' })
    for (const k of ['authorization_endpoint', 'token_endpoint', 'jwks_uri']) {
      await expect(setup({ discovery: { [k]: 'https://evil.example/x' } }).oidc.authorizationUrl({ state: 's', nonce: 'n', challenge: 'c' }), k)
        .rejects.toMatchObject({ kind: 'protocol' })
    }
  })
})

describe('finishing a login', () => {
  it('trades the code and the verifier for tokens, as a public client', async () => {
    const { oidc, requests } = setup()
    const t = await oidc.exchangeCode('the-code', 'the-verifier', 'the-nonce')
    expect(t.refreshToken).toBe('rt-new')
    expect(t.claims.sub).toBe('sub-alice')
    const call = requests.find(r => r.url.endsWith('/token'))!
    expect(Object.fromEntries(new URLSearchParams(String(call.init?.body)))).toEqual({
      grant_type: 'authorization_code',
      code: 'the-code',
      redirect_uri: REDIRECT,
      code_verifier: 'the-verifier',
      client_id: 'kubernetes',
    })
    expect(call.init?.method).toBe('POST')
    expect(new Headers(call.init?.headers).get('authorization')).toBeNull()
    expect(call.init?.redirect).toBe('error')
  })

  it('refuses an ID token that isn\'t for this login', async () => {
    const { oidc } = setup()
    await expect(oidc.exchangeCode('c', 'v', 'another-nonce')).rejects.toMatchObject({ kind: 'protocol' })
  })

  it('says the login is over when the provider refuses, and to try again when it\'s down', async () => {
    const refused = setup({ token: () => ({ status: 400, body: { error: 'invalid_grant', error_description: 'The refresh token was already used.' } }) })
    const e = await refused.oidc.refresh('rt-old').catch(x => x)
    expect(e).toBeInstanceOf(OidcError)
    expect(e.kind).toBe('denied')
    expect(e.message).toContain('invalid_grant')
    for (const status of [500, 502, 503, 429]) {
      await expect(setup({ token: () => ({ status, body: null }) }).oidc.refresh('rt'), String(status)).rejects.toMatchObject({ kind: 'unavailable' })
    }
  })

  it('never puts a token into an error', async () => {
    const { oidc, p, claims } = setup()
    const bad = p.idToken(claims({ aud: 'grafana' }))
    const e = await oidc.verifyIdToken(bad, 'the-nonce').catch(x => x)
    expect(e.message).not.toContain(bad.split('.')[1])
    const refused = setup({ token: () => ({ status: 400, body: { error: 'invalid_grant' } }) })
    const e2 = await refused.oidc.refresh('rt-super-secret').catch(x => x)
    expect(e2.message).not.toContain('rt-super-secret')
  })

  it('refreshes with the refresh token only', async () => {
    const { oidc, requests } = setup()
    // (The made-up provider answers with a login's nonce, which a refresh doesn't check.)
    const t = await oidc.refresh('rt-1')
    expect(t.refreshToken).toBe('rt-new')
    const call = requests.find(r => r.url.endsWith('/token'))!
    expect(Object.fromEntries(new URLSearchParams(String(call.init?.body)))).toEqual({
      grant_type: 'refresh_token', refresh_token: 'rt-1', client_id: 'kubernetes',
    })
  })

  it('hands a refresh token back on log out, and shrugs if that fails', async () => {
    const { oidc, requests } = setup()
    await oidc.revoke('rt-1')
    const call = requests.find(r => r.url.endsWith('/revocation'))!
    expect(new URLSearchParams(String(call.init?.body)).get('token')).toBe('rt-1')
    const down = new Oidc({ issuer: ISSUER, clientId: 'kubernetes', redirectUri: REDIRECT, fetch: (async () => { throw new Error('down') }) as any })
    await expect(down.revoke('rt')).resolves.toBeUndefined()
  })
})

describe('checking an ID token', () => {
  it('accepts the provider\'s own', async () => {
    const { oidc, p, claims } = setup()
    expect((await oidc.verifyIdToken(p.idToken(claims()), 'the-nonce')).sub).toBe('sub-alice')
    expect((await oidc.verifyIdToken(p.idToken(claims({ aud: 'kubernetes' })))).sub).toBe('sub-alice')
  })

  it('refuses anything else', async () => {
    const { oidc, p, claims } = setup()
    const other = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey
    const good = p.idToken(claims())
    const [h, , s] = good.split('.')
    const forgedBody = Buffer.from(JSON.stringify(claims({ sub: 'sub-admin' }))).toString('base64url')
    // "none" and HS256 keyed with the public key: the classic ways to forge a token.
    const head = Buffer.from(JSON.stringify({ alg: 'HS256', kid: 'main' })).toString('base64url')
    const hmac = createHmac('sha256', JSON.stringify(p.jwks().keys[0])).update(`${head}.${forgedBody}`).digest('base64url')
    const cases: [string, string][] = [
      ['signed by someone else', p.idToken(claims(), { key: other })],
      ['claims swapped under a real signature', `${h}.${forgedBody}.${s}`],
      ['alg none', p.idToken(claims(), { alg: 'none', signature: '' })],
      ['alg HS256', `${head}.${forgedBody}.${hmac}`],
      ['unknown key', p.idToken(claims(), { kid: 'rotated-away' })],
      ['another issuer', p.idToken(claims({ iss: 'https://evil.example' }))],
      ['another client', p.idToken(claims({ aud: ['grafana'] }))],
      ['two audiences, authorized for another', p.idToken(claims({ aud: ['kubernetes', 'grafana'], azp: 'grafana' }))],
      ['expired', p.idToken(claims({ exp: nowS - 1 }))],
      ['no expiry', p.idToken(claims({ exp: undefined }))],
      ['from the future', p.idToken(claims({ iat: nowS + 3600 }))],
      ['nobody', p.idToken(claims({ sub: '' }))],
      ['not a JWT', 'abc'],
      ['empty', ''],
    ]
    for (const [name, token] of cases) {
      await expect(oidc.verifyIdToken(token, 'the-nonce'), name).rejects.toBeInstanceOf(OidcError)
    }
    await expect(oidc.verifyIdToken(good, 'the-nonce')).resolves.toBeTruthy()
  })

  it('reads a token\'s claims without trusting them', () => {
    const { p, claims } = setup()
    expect(decodeJwtPayload(p.idToken(claims()))?.sub).toBe('sub-alice')
    expect(decodeJwtPayload('nope')).toBeNull()
    expect(decodeJwtPayload('a.!!!.c')).toBeNull()
  })
})
