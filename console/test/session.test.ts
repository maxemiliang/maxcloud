import { randomBytes } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { OidcError, type TokenSet } from '../server/lib/oidc'
import {
  cookieNames, MAX_AGE, MAX_COOKIE_VALUE, openLogin, openSession, Refresher, safeNext, sameSecret, sealLogin, sealSession,
  type Session,
} from '../server/lib/session'

const keys = [randomBytes(32)]
const T0 = 1_800_000_000_000 // ms
const s0 = T0 / 1000

const session = (over: Partial<Session> = {}): Session => ({
  v: 1, user: 'alice', sub: 'sub-alice', idToken: 'id-1', refreshToken: 'rt-1', idExp: s0 + 3600, loginAt: s0, ...over,
})

const tokens = (n: number, exp: number, sub = 'sub-alice'): TokenSet => ({
  idToken: `id-${n}`, refreshToken: `rt-${n}`, claims: { iss: 'i', sub, aud: 'kubernetes', exp },
})

/** A provider whose refresh tokens work once, as Authelia's do. */
function provider(now: () => number) {
  let n = 1
  const used = new Set<string>()
  const calls: string[] = []
  let gate: Promise<void> = Promise.resolve()
  return {
    calls,
    hold() {
      let open!: () => void
      gate = new Promise<void>((r) => { open = r })
      return open
    },
    refresh: async (rt: string): Promise<TokenSet> => {
      calls.push(rt)
      await gate
      if (used.has(rt) || rt !== `rt-${n}`) throw new OidcError('denied', 'the login provider refused (invalid_grant)')
      used.add(rt)
      n++
      return tokens(n, now() / 1000 + 3600)
    },
  }
}

describe('the session cookie', () => {
  it('round-trips, and ends 12 hours after the login', () => {
    const v = sealSession(keys, session())
    expect(openSession(keys, v, T0 + 1000)).toEqual(session())
    expect(openSession(keys, v, T0 + MAX_AGE * 1000 - 1000)).not.toBeNull()
    expect(openSession(keys, v, T0 + MAX_AGE * 1000)).toBeNull()
  })

  it('isn\'t a login cookie, and the other way round', () => {
    const login = sealLogin(keys, { state: 's', nonce: 'n', verifier: 'v', next: '/', exp: s0 + 600 })
    expect(openSession(keys, login, T0)).toBeNull()
    expect(openLogin(keys, sealSession(keys, session()), T0)).toBeNull()
    expect(openLogin(keys, login, T0)?.state).toBe('s')
    expect(openLogin(keys, login, T0 + 601_000)).toBeNull()
  })

  it('refuses to make a cookie a browser would drop', () => {
    expect(() => sealSession(keys, session({ idToken: 'x'.repeat(MAX_COOKIE_VALUE) }))).toThrow(/doesn't fit/)
  })

  it('fits an ID token of ordinary size with room to spare', () => {
    expect(sealSession(keys, session({ idToken: 'x'.repeat(1400), refreshToken: 'y'.repeat(120) })).length).toBeLessThan(2400)
  })

  it('is a __Host- cookie over https', () => {
    expect(cookieNames(true)).toEqual({ session: '__Host-mc_session', login: '__Host-mc_login' })
    expect(cookieNames(false).session).toBe('mc_session')
  })
})

describe('where to go after logging in', () => {
  const origin = 'https://console.example.com'
  it('keeps paths on this site', () => {
    expect(safeNext('/apps/shop', origin)).toBe('/apps/shop')
    expect(safeNext('/apps/shop?x=1', origin)).toBe('/apps/shop?x=1')
    expect(safeNext('/', origin)).toBe('/')
  })
  it('turns everything else into /', () => {
    for (const bad of [
      'https://evil.example/', '//evil.example/', '/\\evil.example', '\\\\evil.example', 'evil.example', 'javascript:alert(1)',
      '/\tfoo', '/foo\nbar', ' /apps', '', undefined, null, 42, ['/apps'], `/${'a'.repeat(600)}`,
      '/login', '/login?next=/login', '/auth/logout', '/auth/callback?code=x', '/callback', '/api/apps', 'http://console.example.com/apps',
      'https://console.example.com.evil.example/', '/%2F%2Fevil.example'.replace('%2F%2F', '/'),
    ]) {
      expect(safeNext(bad, origin), String(bad)).toBe('/')
    }
  })
  it('never leaves the origin', () => {
    for (const tricky of ['/@evil.example', '/.evil.example', '/apps/../../x', '/%2e%2e/x', '/apps?next=//evil.example']) {
      const out = safeNext(tricky, origin)
      expect(new URL(out, origin).origin, tricky).toBe(origin)
      expect(out.startsWith('/') && !out.startsWith('//'), tricky).toBe(true)
    }
  })
})

describe('comparing secrets', () => {
  it('tells equal from different, whatever the lengths', () => {
    expect(sameSecret('abc', 'abc')).toBe(true)
    expect(sameSecret('abc', 'abd')).toBe(false)
    expect(sameSecret('abc', 'abcd')).toBe(false)
    expect(sameSecret('', 'a')).toBe(false)
  })
})

describe('keeping the ID token fresh', () => {
  it('leaves a token alone while it has more than five minutes', async () => {
    let now = T0
    const p = provider(() => now)
    const r = new Refresher(p.refresh, () => now)
    now = T0 + 3000_000 // 50 minutes in: 10 left
    expect(await r.fresh(session())).toEqual({ kind: 'ok', session: session(), changed: false })
    expect(p.calls).toEqual([])
  })

  it('refreshes inside the last five minutes, and keeps who and when', async () => {
    let now = T0
    const p = provider(() => now)
    const r = new Refresher(p.refresh, () => now)
    now = T0 + 3400_000
    const f = await r.fresh(session())
    expect(f).toEqual({
      kind: 'ok', changed: true,
      session: session({ idToken: 'id-2', refreshToken: 'rt-2', idExp: now / 1000 + 3600 }),
    })
    expect(p.calls).toEqual(['rt-1'])
  })

  it('refreshes once when a page\'s requests arrive together', async () => {
    let now = T0
    const p = provider(() => now)
    const r = new Refresher(p.refresh, () => now)
    now = T0 + 3700_000 // the ID token has run out
    const open = p.hold()
    const all = Promise.all(Array.from({ length: 8 }, () => r.fresh(session())))
    open()
    const results = await all
    // One use of the refresh token; all eight leave with the same new tokens.
    expect(p.calls).toEqual(['rt-1'])
    for (const f of results) {
      expect(f.kind).toBe('ok')
      expect(f.kind === 'ok' && f.session.refreshToken).toBe('rt-2')
      expect(f.kind === 'ok' && f.changed).toBe(true)
    }
  })

  it('gives a late request with the old cookie the same tokens, not a second refresh', async () => {
    let now = T0
    const p = provider(() => now)
    const r = new Refresher(p.refresh, () => now)
    now = T0 + 3700_000
    await r.fresh(session())
    now += 30_000 // sent before the new cookie reached the browser
    const late = await r.fresh(session())
    expect(late.kind === 'ok' && late.session.idToken).toBe('id-2')
    expect(p.calls).toEqual(['rt-1'])
  })

  it('doesn\'t burn the session: the next refresh uses the new token and works', async () => {
    let now = T0
    const p = provider(() => now)
    const r = new Refresher(p.refresh, () => now)
    now = T0 + 3700_000
    const [a, b] = await Promise.all([r.fresh(session()), r.fresh(session())])
    expect(a.kind === 'ok' && b.kind === 'ok').toBe(true)
    const current = (a as { session: Session }).session
    now += 3400_000
    const again = await r.fresh(current)
    expect(again.kind === 'ok' && again.session.refreshToken).toBe('rt-3')
    expect(p.calls).toEqual(['rt-1', 'rt-2'])
  })

  it('forgets a refresh after the grace period, and stays small', async () => {
    let now = T0
    const p = provider(() => now)
    const r = new Refresher(p.refresh, () => now, 60_000)
    now = T0 + 3700_000
    await r.fresh(session())
    expect(r.size).toBe(1)
    now += 61_000
    // The old cookie, long after: its refresh token was used, so the provider says no.
    expect(await r.fresh(session())).toEqual({ kind: 'over' })
    expect(p.calls).toEqual(['rt-1', 'rt-1'])
  })

  it('ends the session for everyone waiting when the provider refuses', async () => {
    const now = T0 + 3700_000
    let calls = 0
    const r = new Refresher(async () => {
      calls++
      throw new OidcError('denied', 'invalid_grant')
    }, () => now)
    const results = await Promise.all([r.fresh(session()), r.fresh(session()), r.fresh(session())])
    expect(results).toEqual([{ kind: 'over' }, { kind: 'over' }, { kind: 'over' }])
    expect(calls).toBe(1)
  })

  it('keeps the session when the provider can\'t be reached and the token still works', async () => {
    const now = T0 + 3400_000 // 200 s left
    let calls = 0
    const r = new Refresher(async () => {
      calls++
      throw new OidcError('unavailable', 'down')
    }, () => now)
    expect(await r.fresh(session())).toEqual({ kind: 'ok', session: session(), changed: false })
    // Not remembered: the next request asks again.
    expect(await r.fresh(session())).toEqual({ kind: 'ok', session: session(), changed: false })
    expect(calls).toBe(2)
    expect(r.size).toBe(0)
  })

  it('says so, and keeps the cookie, when the provider is down and the token has run out', async () => {
    const now = T0 + 3700_000
    const r = new Refresher(async () => {
      throw new OidcError('unavailable', 'down')
    }, () => now)
    expect(await r.fresh(session())).toEqual({ kind: 'unavailable' })
  })

  it('ends the session if a refresh comes back for someone else', async () => {
    const now = T0 + 3700_000
    const r = new Refresher(async () => tokens(2, now / 1000 + 3600, 'sub-mallory'), () => now)
    expect(await r.fresh(session())).toEqual({ kind: 'over' })
  })

  it('ends the session if the answer can\'t be trusted', async () => {
    const now = T0 + 3700_000
    const r = new Refresher(async () => {
      throw new OidcError('protocol', 'the ID token has a bad signature')
    }, () => now)
    expect(await r.fresh(session())).toEqual({ kind: 'over' })
  })

  it('ends 12 hours after the login, however fresh the token', async () => {
    const now = T0 + MAX_AGE * 1000
    const r = new Refresher(async () => tokens(2, now / 1000 + 3600), () => now)
    expect(await r.fresh(session({ idExp: now / 1000 + 3000 }))).toEqual({ kind: 'over' })
  })

  it('lives as long as the ID token when there\'s no refresh token', async () => {
    let now = T0 + 3400_000
    const r = new Refresher(async () => {
      throw new Error('never asked')
    }, () => now)
    const s = session({ refreshToken: '' })
    expect(await r.fresh(s)).toEqual({ kind: 'ok', session: s, changed: false })
    now = T0 + 3600_000
    expect(await r.fresh(s)).toEqual({ kind: 'over' })
  })
})
