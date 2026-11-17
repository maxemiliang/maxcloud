// The built server, end to end, without a cluster: logging in through a
// made-up Authelia, reading a made-up API server as the user, asking a
// made-up apps API. `npm run build` first, then `npm run test:e2e`.
import { randomBytes } from 'node:crypto'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { isAllowed } from '../../server/lib/kube'
import { Browser, fakeAppsApi, fakeIdp, fakeKube, freePort, runConsole, startConsole, type Console, type FakeAppsApi, type FakeIdp, type FakeKube } from './fakes'

const built = existsSync(join(dirname(fileURLToPath(import.meta.url)), '../../.output/server/index.mjs'))
const KEY = randomBytes(32).toString('base64')

let idp: FakeIdp
let kube: FakeKube
let api: FakeAppsApi
let server: Console
let env: Record<string, string>

const cookieAttrs = (res: Response, name: string) => res.headers.getSetCookie().find(c => c.startsWith(`${name}=`)) ?? ''

beforeAll(async () => {
  if (!built) throw new Error('no build: run `npm run build` before `npm run test:e2e`')
  const port = await freePort()
  const origin = `http://127.0.0.1:${port}`
  idp = await fakeIdp('kubernetes', () => [`${origin}/auth/callback`])
  kube = await fakeKube(() => idp)
  api = await fakeAppsApi()
  env = {
    CONSOLE_ORIGIN: origin,
    CONSOLE_SESSION_KEY: KEY,
    CONSOLE_OIDC_ISSUER: idp.url,
    CONSOLE_KUBE_URL: kube.url,
    CONSOLE_APPS_API: `${api.url}/api/v1/apps/`,
  }
  server = await startConsole(env, port)
})

afterAll(async () => {
  await server?.stop()
  await Promise.all([idp?.close(), kube?.close(), api?.close()])
})

const browser = () => new Browser(server.url)

/** A browser that has logged in as user. */
async function loggedIn(user = 'alice'): Promise<Browser> {
  idp.user = user
  const b = browser()
  const res = await b.login()
  expect(res.status).toBe(303)
  expect(b.cookies.has('mc_session')).toBe(true)
  idp.user = 'alice'
  return b
}

describe('without a login', () => {
  it('sends pages to the login page, and remembers where you were going', async () => {
    const b = browser()
    const home = await b.request('/')
    expect([home.status, home.headers.get('location')]).toEqual([302, '/login'])
    const app = await b.request('/apps/shop')
    expect([app.status, app.headers.get('location')]).toEqual([302, '/login?next=%2Fapps%2Fshop'])
  })

  it('answers the API with 401 and nothing else', async () => {
    const b = browser()
    for (const path of ['/api/me', '/api/apps', '/api/apps/shop', '/api/apps/shop/logs', '/api/apps/shop/revisions', '/api/nosuch']) {
      const res = await b.request(path)
      expect(res.status, path).toBe(401)
      expect(await res.text()).not.toContain('shop--staging')
    }
    expect(kube.requests).toEqual([])
  })

  it('serves the login page with a strict Content-Security-Policy', async () => {
    const res = await browser().request('/login')
    expect(res.status).toBe(200)
    const html = await res.text()
    const csp = res.headers.get('content-security-policy') ?? ''
    const nonce = /script-src 'self' 'nonce-([A-Za-z0-9+/=]+)'/.exec(csp)?.[1]
    expect(nonce, csp).toBeTruthy()
    expect(csp).toContain('default-src \'none\'')
    expect(csp).toContain('style-src \'self\'')
    expect(csp).toContain('connect-src \'self\'')
    expect(csp).toContain('frame-ancestors \'none\'')
    expect(csp).toContain('base-uri \'none\'')
    // Logging in redirects the form post to the provider: Chrome checks that too.
    expect(csp).toContain(`form-action 'self' ${idp.url}`)
    expect(csp).not.toMatch(/unsafe-inline|unsafe-eval|\*|https?:\/\/(?!127\.0\.0\.1)/)
    // Every script on the page carries this response's nonce; nothing inline besides.
    const scripts = html.match(/<script[\s>][^>]*>/g) ?? []
    expect(scripts.length).toBeGreaterThan(0)
    for (const s of scripts) expect(s).toContain(`nonce="${nonce}"`)
    expect(html).not.toMatch(/\son[a-z]+="/)
    expect(html).not.toMatch(/<style[\s>]/)
    expect(html).not.toMatch(/\sstyle="/)
    // Nothing from anywhere else.
    expect(html).not.toMatch(/(src|href)="(https?:)?\/\//)
    // A new nonce every time.
    const again = (await browser().request('/login')).headers.get('content-security-policy')
    expect(again).not.toBe(csp)
  })

  it('sets the headers that keep pages and answers to themselves', async () => {
    const b = await loggedIn()
    const js = /src="(\/_nuxt\/[^"]+\.js)"/.exec(await (await b.request('/')).text())![1]!
    for (const path of ['/login', '/', '/api/me', '/api/apps', '/healthz', '/icon.svg', js]) {
      const res = await (path === '/login' ? browser() : b).request(path)
      const h = res.headers
      expect(res.status, path).toBe(200)
      expect(h.get('x-content-type-options'), path).toBe('nosniff')
      expect(h.get('x-frame-options'), path).toBe('DENY')
      expect(h.get('referrer-policy'), path).toBe('same-origin')
      expect(h.get('cross-origin-opener-policy'), path).toBe('same-origin')
      expect(h.get('cross-origin-resource-policy'), path).toBe('same-origin')
      expect(h.get('content-security-policy'), path).toContain('frame-ancestors \'none\'')
      // Only the build's hashed files may be kept by a browser or a proxy.
      expect(h.get('cache-control'), path).toBe(path === js ? 'public, max-age=31536000, immutable' : 'no-store')
    }
    // Errors too (Nitro's own error answers are stricter still).
    const denied = (await browser().request('/api/me')).headers
    expect(denied.get('x-content-type-options')).toBe('nosniff')
    expect(denied.get('content-security-policy')).toContain('frame-ancestors \'none\'')
    expect(denied.get('cache-control')).toBe('no-store')
  })

  it('says it\'s alive without saying anything else', async () => {
    const res = await browser().request('/healthz')
    expect([res.status, await res.text()]).toEqual([200, 'ok'])
  })
})

describe('changing nothing', () => {
  it('takes no method but GET anywhere except logging in and out', async () => {
    const b = await loggedIn()
    for (const [method, path] of [['POST', '/api/apps'], ['PUT', '/api/apps/shop'], ['DELETE', '/api/apps/shop'], ['PATCH', '/api/me'], ['POST', '/'], ['POST', '/auth/callback'], ['DELETE', '/auth/logout']] as const) {
      const res = await b.request(path, { method, form: { a: '1' } })
      expect(res.status, `${method} ${path}`).toBe(405)
    }
  })

  it('refuses a login or logout posted from another site, or from nowhere', async () => {
    const b = await loggedIn()
    for (const path of ['/auth/login', '/auth/logout']) {
      for (const origin of ['https://evil.example', 'null', `${server.url}.evil.example`, null]) {
        const res = await b.request(path, { method: 'POST', form: {}, origin })
        expect(res.status, `${path} from ${origin}`).toBe(403)
      }
    }
    // Still logged in: the forged logout did nothing.
    expect((await b.request('/api/me')).status).toBe(200)
  })

  it('refuses a body larger than a login form', async () => {
    const res = await browser().request('/auth/login', { method: 'POST', form: { next: 'x'.repeat(10_000) } })
    expect(res.status).toBe(413)
  })
})

describe('logging in', () => {
  it('sends the browser to the provider with PKCE, and keeps the secrets in a sealed cookie', async () => {
    const b = browser()
    const res = await b.login('/apps/shop', false)
    expect(res.status).toBe(303)
    const to = new URL(res.headers.get('location')!)
    expect(to.origin + to.pathname).toBe(`${idp.url}/api/oidc/authorization`)
    const q = Object.fromEntries(to.searchParams)
    expect(q).toMatchObject({
      response_type: 'code', client_id: 'kubernetes', redirect_uri: `${server.url}/auth/callback`,
      scope: 'openid profile email groups offline_access', code_challenge_method: 'S256',
    })
    expect(q.state).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(q.nonce).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(q.code_challenge).toMatch(/^[A-Za-z0-9_-]{43}$/)
    const cookie = cookieAttrs(res, 'mc_login')
    expect(cookie).toMatch(/; HttpOnly/i)
    expect(cookie).toMatch(/; SameSite=Lax/i)
    expect(cookie).toMatch(/; Path=\//i)
    expect(cookie).toMatch(/; Max-Age=600/i)
    // The browser can't read the state or the verifier out of it.
    const value = b.cookies.get('mc_login')!
    expect(Buffer.from(value, 'base64url').toString('latin1')).not.toContain(q.state)
  })

  it('finishes at the page you were going to, with a session only the server can read', async () => {
    const b = browser()
    const res = await b.login('/apps/shop')
    expect([res.status, res.headers.get('location')]).toEqual([303, '/apps/shop'])
    const cookie = cookieAttrs(res, 'mc_session')
    expect(cookie).toMatch(/; HttpOnly/i)
    expect(cookie).toMatch(/; SameSite=Lax/i)
    expect(cookie).toMatch(/; Path=\//i)
    expect(cookie).not.toMatch(/Domain=/i)
    // The login cookie is used up.
    expect(b.cookies.has('mc_login')).toBe(false)
    expect(await (await b.request('/api/me')).json()).toEqual({ user: 'alice', mock: false })
  })

  it('never lets a token reach the browser', async () => {
    const b = await loggedIn()
    const tokens = kube.requests.map(r => r.token).filter(Boolean)
    const idToken = tokens[tokens.length - 1]!
    expect(idToken.split('.')).toHaveLength(3)
    const sealed = b.cookies.get('mc_session')!
    const raw = Buffer.from(sealed, 'base64url').toString('latin1')
    for (const part of idToken.split('.')) expect(raw).not.toContain(part)
    for (const path of ['/', '/apps/shop', '/api/me', '/api/apps', '/api/apps/shop', '/api/apps/shop/revisions', '/api/apps/shop/logs']) {
      const res = await b.request(path)
      const text = await res.text() + JSON.stringify([...res.headers])
      expect(res.status, path).toBe(200)
      for (const part of idToken.split('.')) expect(text, path).not.toContain(part)
      expect(text, path).not.toMatch(/rt_[A-Za-z0-9_-]{20}/)
    }
  })

  it('only goes to a page on this site afterwards', async () => {
    for (const next of ['https://evil.example/', '//evil.example', '/\\evil.example', 'javascript:alert(1)', '/login', '/auth/logout']) {
      const res = await browser().login(next)
      expect([res.status, res.headers.get('location')], next).toEqual([303, '/'])
    }
  })

  it('refuses a callback this browser didn\'t start', async () => {
    // Someone else's login: their state and code, sent to a victim's browser.
    const attacker = browser()
    const start = await attacker.login('/', false)
    const atIdp = await fetch(start.headers.get('location')!, { redirect: 'manual' })
    const theirs = atIdp.headers.get('location')!

    // A browser with no login in flight.
    const fresh = browser()
    const a = await fresh.request(theirs)
    expect([a.status, a.headers.get('location')]).toEqual([303, '/login?error=expired'])
    expect(fresh.cookies.has('mc_session')).toBe(false)

    // A browser with a login of its own in flight: another state.
    const victim = browser()
    await victim.login('/', false)
    const v = await victim.request(theirs)
    expect([v.status, v.headers.get('location')]).toEqual([303, '/login?error=state'])
    expect(victim.cookies.has('mc_session')).toBe(false)
    // And that attempt used up the victim's own login cookie.
    expect(victim.cookies.has('mc_login')).toBe(false)
  })

  it('refuses a callback played twice', async () => {
    const b = browser()
    const start = await b.login('/', false)
    const loginCookie = b.header()
    const atIdp = await fetch(start.headers.get('location')!, { redirect: 'manual' })
    const back = atIdp.headers.get('location')!
    expect((await b.request(back)).headers.get('location')).toBe('/')
    // The same URL with the same login cookie, replayed: the code was used.
    const again = await browser().request(back, { cookie: loginCookie })
    expect(again.headers.get('location')).toBe('/login?error=provider')
    expect(cookieAttrs(again, 'mc_session')).toBe('')
  })

  it('refuses a callback without a code, or with a made-up one', async () => {
    const b = browser()
    const start = await b.login('/', false)
    const state = new URL(start.headers.get('location')!).searchParams.get('state')!
    const cookie = b.header()
    for (const [query, error] of [[`state=${state}`, 'provider'], [`state=${state}&code=made-up`, 'provider'], ['code=x', 'state'], [`state=${state}&error=access_denied`, 'denied'], [`state=${state}&error=server_error`, 'provider']]) {
      const res = await browser().request(`/auth/callback?${query}`, { cookie })
      expect(res.headers.get('location'), query).toBe(`/login?error=${error}`)
      expect(cookieAttrs(res, 'mc_session'), query).toBe('')
    }
  })

  it('says so when the provider turns the user away', async () => {
    idp.user = 'nobody'
    const res = await browser().login()
    idp.user = 'alice'
    expect(res.headers.get('location')).toBe('/login?error=denied')
  })

  it('lets in people only, and only ones the cluster accepts', async () => {
    for (const user of ['robot', 'stranger']) {
      idp.user = user
      const b = browser()
      const res = await b.login()
      idp.user = 'alice'
      expect(res.headers.get('location'), user).toBe('/login?error=not_allowed')
      expect(b.cookies.has('mc_session'), user).toBe(false)
    }
  })

  it('shows fixed words for an error, never what the address said', async () => {
    const html = await (await browser().request('/login?error=%3Cscript%3Ealert(1)%3C/script%3E&next=javascript:alert(1)')).text()
    expect(html).not.toContain('<script>alert(1)')
    expect(html).toContain('Authelia couldn&#39;t finish the login')
  })

  it('doesn\'t offer the mock\'s users', async () => {
    const html = await (await browser().request('/login')).text()
    expect(html).toContain('Log in with Authelia')
    expect(html).not.toContain('Mock mode')
    const res = await browser().request('/auth/login', { method: 'POST', form: { user: 'admin' } })
    expect(new URL(res.headers.get('location')!).origin).toBe(idp.url)
  })
})

describe('reading the cluster as the user', () => {
  it('renders the apps on the server, for the user who asked', async () => {
    const b = await loggedIn()
    const html = await (await b.request('/')).text()
    for (const app of ['shop', 'notes', 'blog', 'run-demo']) expect(html).toContain(`/apps/${app}`)
    expect(html).toContain('alice')
    const csp = (await b.request('/')).headers.get('content-security-policy') ?? ''
    expect(csp).toMatch(/script-src 'self' 'nonce-/)
  })

  it('shows an owner their app and another developer less', async () => {
    const b = await loggedIn()
    const shop = await (await b.request('/api/apps/shop')).json()
    expect(shop.restricted).toEqual([])
    expect(shop.quota).toHaveLength(3)
    const blog = await (await b.request('/api/apps/blog')).json()
    expect(blog.restricted).toEqual(['pods', 'workloads', 'quota', 'domains', 'problems'])
    expect(blog.quota).toEqual([])
    const page = await (await b.request('/apps/blog')).text()
    expect(page).toContain('not one of this app')
  })

  it('answers 404 for anything that isn\'t an app, without asking the cluster about it', async () => {
    const b = await loggedIn()
    const before = kube.requests.length
    for (const name of ['Shop', 'shop.x', 'shop%2Fsecrets', '..%2Fkube-system', 'a'.repeat(64)]) {
      const res = await b.request(`/api/apps/${name}`)
      expect(res.status, name).toBe(404)
    }
    expect(kube.requests.length).toBe(before)
    expect((await b.request('/api/apps/kube-system')).status).toBe(404)
    expect((await b.request('/api/apps/nosuch')).status).toBe(404)
    expect((await b.request('/apps/nosuch')).status).toBe(404)
  })

  it('only ever asked the cluster for what\'s on its list, with a user\'s own token', () => {
    expect(kube.requests.length).toBeGreaterThan(30)
    for (const r of kube.requests) {
      expect(isAllowed(r.method, r.path), `${r.method} ${r.path}`).toBe(true)
      expect(r.path).not.toMatch(/secrets|configmaps|\/log|exec|token/)
      // Always a person's ID token from the provider, never anything of the server's own.
      expect(r.token.split('.'), r.path).toHaveLength(3)
    }
  })
})

describe('the platform\'s logs, metrics and alerts', () => {
  it('shows them to an owner', async () => {
    const b = await loggedIn()
    const logs = await (await b.request('/api/apps/shop/logs')).json()
    expect(logs.ok).toBe(true)
    expect(logs.data.entries.length).toBeGreaterThan(0)
    expect((await (await b.request('/api/apps/notes/alerts')).json()).data.alerts[0].name).toBe('KnativeRevisionPodsPending')
    expect((await (await b.request('/api/apps/shop/metrics?window=7d')).json()).data.window).toBe('7d')
  })

  it('passes on the user\'s token and only the ranges the console offers', async () => {
    const b = await loggedIn()
    api.requests.length = 0
    await b.request('/api/apps/shop/logs?since=99999d&limit=100000&pod=.*&container=x&all=true&query=%7Bnamespace%3D%22blog%22%7D')
    await b.request('/api/apps/shop/metrics?window=15d%26x%3D1')
    expect(api.requests.map(r => r.path)).toEqual(['/api/v1/apps/shop/logs?since=1h&limit=500', '/api/v1/apps/shop/metrics?window=1h'])
    for (const r of api.requests) expect(r.token.split('.')).toHaveLength(3)
  })

  it('explains, per panel, what it can\'t show, and the page still renders', async () => {
    const b = await loggedIn()
    expect(await (await b.request('/api/apps/blog/logs')).json()).toEqual({ ok: false, reason: 'forbidden', message: 'Only the app\'s owners and admins see its logs.' })

    api.mode = 'not_routed'
    for (const f of ['logs', 'metrics', 'alerts']) {
      expect(await (await b.request(`/api/apps/shop/${f}`)).json(), f).toEqual({ ok: false, reason: 'not_offered', message: `The platform doesn't serve apps' ${f} yet.` })
    }
    expect((await b.request('/apps/shop')).status).toBe(200)

    api.mode = 'broken'
    const broken = await (await b.request('/api/apps/shop/logs')).json()
    expect(broken).toMatchObject({ ok: false, reason: 'unavailable' })
    expect((await b.request('/api/apps/shop')).status).toBe(200)
    api.mode = 'ok'
  })
})

describe('the session', () => {
  it('refreshes once when a page\'s requests arrive together, and nobody is logged out', async () => {
    // ID tokens that are already inside the refresh margin: every request
    // wants a new one.
    idp.lifetime = 120
    const b = await loggedIn()
    const old = b.header()
    idp.refreshes.length = 0

    const answers = await Promise.all(Array.from({ length: 12 }, () => b.request('/api/me', { cookie: old })))
    expect(answers.map(r => r.status)).toEqual(Array(12).fill(200))
    // One use of the refresh token for twelve requests.
    expect(idp.refreshes).toHaveLength(1)
    // Each of them leaves with the new cookie.
    for (const r of answers) expect(cookieAttrs(r, 'mc_session')).toMatch(/^mc_session=[A-Za-z0-9_-]+; /)

    // A request that left before the new cookie arrived: same answer, no second use.
    const late = await b.request('/api/apps', { cookie: old })
    expect(late.status).toBe(200)
    expect(idp.refreshes).toHaveLength(1)

    // The browser carries on with the new cookie, and the login is intact:
    // the next refresh uses the rotated token and works.
    b.take(answers[0]!)
    expect(b.header()).not.toBe(old)
    const next = await b.request('/api/apps/shop')
    expect(next.status).toBe(200)
    expect(idp.refreshes).toHaveLength(2)
    expect(idp.refreshes[1]).not.toBe(idp.refreshes[0])
    expect((await b.request('/api/me')).status).toBe(200)
    idp.lifetime = 3600
  })

  it('renders a page on the server through a refresh', async () => {
    idp.lifetime = 120
    const b = await loggedIn()
    idp.refreshes.length = 0
    // The page's own API calls run inside this request, with the old cookie.
    const res = await b.request('/')
    expect(res.status).toBe(200)
    expect(await res.text()).toContain('/apps/shop')
    expect(idp.refreshes).toHaveLength(1)
    expect(b.cookies.get('mc_session')).toBeTruthy()
    expect((await b.request('/api/me')).status).toBe(200)
    idp.lifetime = 3600
  })

  it('ends, and clears the cookie, when the provider won\'t renew it', async () => {
    idp.lifetime = 120
    const b = await loggedIn()
    idp.revokeAll()
    const res = await b.request('/api/me')
    expect(res.status).toBe(401)
    expect(cookieAttrs(res, 'mc_session')).toMatch(/Max-Age=0/i)
    const page = await b.request('/apps/shop')
    expect([page.status, page.headers.get('location')]).toEqual([302, '/login?next=%2Fapps%2Fshop'])
    idp.lifetime = 3600
  })

  it('doesn\'t take a cookie that was changed, or one it didn\'t seal', async () => {
    const b = await loggedIn()
    const value = b.cookies.get('mc_session')!
    const flipped = value.slice(0, 40) + (value[40] === 'A' ? 'B' : 'A') + value.slice(41)
    for (const forged of [flipped, 'AAAA', value.slice(0, -4), Buffer.from(JSON.stringify({ v: 1, user: 'admin' })).toString('base64url')]) {
      const res = await browser().request('/api/me', { cookie: `mc_session=${forged}` })
      expect(res.status).toBe(401)
      expect(cookieAttrs(res, 'mc_session')).toMatch(/Max-Age=0/i)
    }
    // A login cookie isn't a session.
    const other = browser()
    await other.login('/', false)
    expect((await browser().request('/api/me', { cookie: `mc_session=${other.cookies.get('mc_login')}` })).status).toBe(401)
  })

  it('logs out: the cookie goes and the refresh token is handed back', async () => {
    const b = await loggedIn()
    idp.revoked.length = 0
    const res = await b.request('/auth/logout', { method: 'POST', form: {} })
    expect([res.status, res.headers.get('location')]).toEqual([303, '/login?out=1'])
    expect(cookieAttrs(res, 'mc_session')).toMatch(/Max-Age=0/i)
    expect(b.cookies.has('mc_session')).toBe(false)
    expect((await b.request('/api/me')).status).toBe(401)
    await expect.poll(() => idp.revoked.length, { timeout: 3000 }).toBe(1)
    expect(idp.revoked[0]).toMatch(/^rt_/)
    const page = await (await b.request('/login?out=1')).text()
    expect(page).toContain('logged out of the console')
  })

  it('logged nothing secret', () => {
    const out = server.output()
    expect(out).not.toMatch(/eyJ[A-Za-z0-9_-]{10,}\./)
    expect(out).not.toMatch(/rt_[A-Za-z0-9_-]{20}/)
    expect(out).not.toContain(KEY)
  })
})

describe('over https', () => {
  it('uses __Host- cookies marked Secure, and HSTS', async () => {
    const port = await freePort()
    const origin = 'https://console.example.com'
    const https = await startConsole({ ...env, CONSOLE_ORIGIN: origin }, port)
    try {
      const res = await fetch(`${https.url}/auth/login`, { method: 'POST', headers: { 'origin': origin, 'content-type': 'application/x-www-form-urlencoded' }, body: 'next=/', redirect: 'manual' })
      expect(res.status).toBe(303)
      const cookie = cookieAttrs(res, '__Host-mc_login')
      expect(cookie).toMatch(/; Secure/i)
      expect(cookie).toMatch(/; HttpOnly/i)
      expect(cookie).toMatch(/; Path=\//i)
      expect(cookie).not.toMatch(/Domain=/i)
      expect(res.headers.get('strict-transport-security')).toBe('max-age=31536000')
      expect(new URL(res.headers.get('location')!).searchParams.get('redirect_uri')).toBe(`${origin}/auth/callback`)
      // The address it's reached at doesn't make a request same-site.
      const local = await fetch(`${https.url}/auth/login`, { method: 'POST', headers: { origin: https.url }, redirect: 'manual' })
      expect(local.status).toBe(403)
    } finally {
      expect(await https.stop()).toBe(0)
    }
  })
})

describe('starting', () => {
  it('doesn\'t, without a cookie-sealing key', async () => {
    const { CONSOLE_SESSION_KEY: _key, ...rest } = env
    const { code, output } = await runConsole(rest)
    expect(code).toBe(1)
    expect(output).toContain('CONSOLE_SESSION_KEY')
    expect(output).toContain('not starting')
    expect(output).not.toContain('Listening')
  })

  it('doesn\'t, with a weak key or no origin', async () => {
    expect((await runConsole({ ...env, CONSOLE_SESSION_KEY: 'hunter2' })).code).toBe(1)
    const { CONSOLE_ORIGIN: _origin, ...rest } = env
    const { code, output } = await runConsole(rest)
    expect(code).toBe(1)
    expect(output).toContain('CONSOLE_ORIGIN')
  })

  it('stops cleanly when told to', async () => {
    const port = await freePort()
    const c = await startConsole({ CONSOLE_MOCK: '1' }, port)
    expect(c.output()).toContain('mock mode')
    expect(await c.stop()).toBe(0)
  })
})
