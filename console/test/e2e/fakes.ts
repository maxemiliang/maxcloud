// Stand-ins for what the console talks to, on this machine: Authelia (an
// OpenID Connect provider), the Kubernetes API server and the platform's
// apps API. Each records what it was asked.
import { spawn, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { mockAppsApi, mockToken, mockTransport } from '../../server/lib/mock'
import { decodeJwtPayload } from '../../server/lib/oidc'
import { fakeProvider, s256, type FakeProvider } from '../helpers/provider'

const root = join(dirname(fileURLToPath(import.meta.url)), '../..')

function listen(handler: (req: IncomingMessage, res: ServerResponse, body: string) => void | Promise<void>): Promise<{ server: Server, url: string }> {
  const server = createServer((req, res) => {
    let body = ''
    req.on('data', c => (body += c))
    req.on('end', () => void Promise.resolve(handler(req, res, body)).catch((e) => {
      res.writeHead(500)
      res.end(String(e))
    }))
  })
  return new Promise(r => server.listen(0, '127.0.0.1', () => r({ server, url: `http://127.0.0.1:${(server.address() as AddressInfo).port}` })))
}

export async function freePort(): Promise<number> {
  const { server, url } = await listen(() => {})
  await new Promise(r => server.close(r))
  return Number(new URL(url).port)
}

const json = (res: ServerResponse, status: number, body: unknown) => {
  res.writeHead(status, { 'content-type': 'application/json' })
  res.end(JSON.stringify(body))
}

export interface FakeIdp {
  url: string
  provider: FakeProvider
  /** Who "logs in" at the provider when the browser arrives. */
  user: string
  /** How long the ID tokens it issues last, in seconds. */
  lifetime: number
  /** Refresh tokens it has been asked to exchange, in order. */
  refreshes: string[]
  revoked: string[]
  authorizations: URLSearchParams[]
  /** Every refresh token stops working (the user was logged out everywhere). */
  revokeAll: () => void
  close: () => Promise<void>
}

/**
 * Authelia, as far as the console can tell: authorization codes that work
 * once and only with the right PKCE verifier and redirect URI, and refresh
 * tokens that rotate: each works once, and using one twice ends the login.
 */
export async function fakeIdp(clientId: string, redirectUris: () => string[]): Promise<FakeIdp> {
  const codes = new Map<string, { challenge: string, nonce: string, user: string, redirectUri: string }>()
  const live = new Map<string, string>() // refresh token -> user
  const spent = new Set<string>()
  const idp = {} as FakeIdp
  const issue = (user: string, nonce?: string) => {
    const now = Math.floor(Date.now() / 1000)
    const rt = `rt_${randomBytes(24).toString('base64url')}`
    live.set(rt, user)
    return {
      access_token: `at_${randomBytes(12).toString('base64url')}`,
      token_type: 'bearer',
      expires_in: idp.lifetime,
      refresh_token: rt,
      id_token: idp.provider.idToken({
        iss: idp.url, sub: `sub-${user}`, aud: [clientId], exp: now + idp.lifetime, iat: now,
        preferred_username: user, groups: ['maxcloud-developers'], email: `${user}@example.com`, ...(nonce ? { nonce } : {}),
      }),
    }
  }
  const { server, url } = await listen((req, res, body) => {
    const u = new URL(req.url ?? '/', idp.url)
    if (u.pathname === '/.well-known/openid-configuration') return json(res, 200, idp.provider.discovery())
    if (u.pathname === '/jwks.json') return json(res, 200, idp.provider.jwks())
    if (u.pathname === '/api/oidc/authorization') {
      const q = u.searchParams
      idp.authorizations.push(q)
      const redirectUri = q.get('redirect_uri') ?? ''
      if (q.get('client_id') !== clientId || !redirectUris().includes(redirectUri) || q.get('response_type') !== 'code'
        || q.get('code_challenge_method') !== 'S256' || !q.get('code_challenge') || !q.get('state')) {
        // Authelia shows an error page and doesn't redirect.
        res.writeHead(400, { 'content-type': 'text/plain' })
        return res.end('invalid authorization request')
      }
      const back = new URL(redirectUri)
      if (idp.user === 'nobody') {
        back.search = new URLSearchParams({ error: 'access_denied', state: q.get('state')! }).toString()
      } else {
        const code = `code_${randomBytes(18).toString('base64url')}`
        codes.set(code, { challenge: q.get('code_challenge')!, nonce: q.get('nonce') ?? '', user: idp.user, redirectUri })
        back.search = new URLSearchParams({ code, state: q.get('state')!, iss: idp.url }).toString()
      }
      res.writeHead(302, { location: back.href })
      return res.end()
    }
    if (u.pathname === '/api/oidc/token' && req.method === 'POST') {
      const f = new URLSearchParams(body)
      if (f.get('client_id') !== clientId) return json(res, 401, { error: 'invalid_client' })
      if (f.get('grant_type') === 'authorization_code') {
        const c = codes.get(f.get('code') ?? '')
        codes.delete(f.get('code') ?? '')
        if (!c || c.redirectUri !== f.get('redirect_uri') || s256(f.get('code_verifier') ?? '') !== c.challenge) {
          return json(res, 400, { error: 'invalid_grant', error_description: 'The authorization code is invalid or was already used.' })
        }
        return json(res, 200, issue(c.user, c.nonce))
      }
      if (f.get('grant_type') === 'refresh_token') {
        const rt = f.get('refresh_token') ?? ''
        idp.refreshes.push(rt)
        const user = live.get(rt)
        if (!user) {
          // A refresh token used twice: the whole login goes.
          if (spent.has(rt)) live.clear()
          return json(res, 400, { error: 'invalid_grant', error_description: 'The refresh token is invalid or was already used.' })
        }
        live.delete(rt)
        spent.add(rt)
        return json(res, 200, issue(user))
      }
      return json(res, 400, { error: 'unsupported_grant_type' })
    }
    if (u.pathname === '/api/oidc/revocation' && req.method === 'POST') {
      const token = new URLSearchParams(body).get('token') ?? ''
      idp.revoked.push(token)
      live.delete(token)
      return json(res, 200, {})
    }
    json(res, 404, {})
  })
  Object.assign(idp, {
    url, provider: fakeProvider(url), user: 'alice', lifetime: 3600, refreshes: [], revoked: [], authorizations: [],
    revokeAll: () => live.clear(),
    close: () => new Promise<void>(r => server.close(() => r())),
  })
  return idp
}

export interface FakeKube {
  url: string
  /** Every request: its method, path and the bearer token it carried. */
  requests: { method: string, path: string, token: string }[]
  close: () => Promise<void>
}

/**
 * The API server: accepts the provider's unexpired ID tokens and answers as
 * the platform's RBAC would for that user (the mock's made-up cluster).
 */
export async function fakeKube(idp: () => FakeIdp): Promise<FakeKube> {
  const transport = mockTransport()
  const kube = { requests: [] } as unknown as FakeKube
  const { server, url } = await listen(async (req, res, body) => {
    const token = (req.headers.authorization ?? '').replace(/^Bearer /, '')
    kube.requests.push({ method: req.method ?? '', path: req.url ?? '', token })
    const claims = decodeJwtPayload(token)
    const user = typeof claims?.preferred_username === 'string' ? claims.preferred_username : ''
    const expired = typeof claims?.exp !== 'number' || claims.exp * 1000 <= Date.now()
    if (!claims || claims.iss !== idp().url || expired || user === 'stranger') {
      return json(res, 401, { kind: 'Status', code: 401, reason: 'Unauthorized' })
    }
    if (req.method === 'POST' && req.url === '/apis/authentication.k8s.io/v1/selfsubjectreviews') {
      // A login whose username has no "authelia:" prefix isn't a person's.
      const username = user === 'robot' ? 'system:serviceaccount:shop:default' : `authelia:${user}`
      return json(res, 201, { status: { userInfo: { username, groups: ['authelia:maxcloud-developers', 'system:authenticated'] } } })
    }
    const out = await transport({ method: req.method as 'GET', path: req.url ?? '/', token: mockToken(user), body: body || undefined })
    json(res, out.status, out.body ?? {})
  })
  kube.url = url
  kube.close = () => new Promise<void>(r => server.close(() => r()))
  return kube
}

export interface FakeAppsApi {
  url: string
  /** not_routed: a platform without the API (Traefik's 404). broken: Loki is down. */
  mode: 'ok' | 'not_routed' | 'broken'
  requests: { path: string, token: string }[]
  close: () => Promise<void>
}

/** The maxcloud controller's apps API: owners and admins get answers, others 403. */
export async function fakeAppsApi(): Promise<FakeAppsApi> {
  const api = { mode: 'ok', requests: [] } as unknown as FakeAppsApi
  const { server, url } = await listen((req, res) => {
    const token = (req.headers.authorization ?? '').replace(/^Bearer /, '')
    api.requests.push({ path: req.url ?? '', token })
    if (api.mode === 'not_routed') {
      res.writeHead(404, { 'content-type': 'text/plain' })
      return res.end('404 page not found\n')
    }
    if (api.mode === 'broken') return json(res, 502, { error: 'loki: connection refused' })
    const u = new URL(req.url ?? '/', 'http://x')
    const m = u.pathname.match(/^\/api\/v1\/apps\/([^/]+)\/(logs|metrics|alerts)$/)
    const user = String(decodeJwtPayload(token)?.preferred_username ?? '')
    if (!m || !user) return json(res, m ? 401 : 404, { error: m ? 'no token' : 'not found' })
    const out = mockAppsApi(Date.now(), mockToken(user), m[1]!, m[2] as 'logs', u.searchParams)
    if (out.ok) return json(res, 200, out.data)
    json(res, out.reason === 'forbidden' ? 403 : 404, { error: out.reason === 'forbidden' ? `authelia:${user} may not list pods in ${m[1]}` : `no app ${m[1]}` })
  })
  api.url = url
  api.close = () => new Promise<void>(r => server.close(() => r()))
  return api
}

export interface Console {
  url: string
  output: () => string
  stop: () => Promise<number | null>
}

/** Starts the built server (.output) with env, and waits until it answers. */
export async function startConsole(env: Record<string, string>, port: number): Promise<Console> {
  let out = ''
  const child: ChildProcess = spawn(process.execPath, [join(root, '.output/server/index.mjs')], {
    env: { PATH: process.env.PATH ?? '', HOST: '127.0.0.1', PORT: String(port), ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  child.stdout!.on('data', c => (out += c))
  child.stderr!.on('data', c => (out += c))
  const exited = new Promise<number | null>(r => child.on('exit', code => r(code)))
  const url = `http://127.0.0.1:${port}`
  const deadline = Date.now() + 15_000
  for (;;) {
    if (child.exitCode !== null) throw new Error(`the console exited with ${child.exitCode}:\n${out}`)
    const ok = await fetch(`${url}/healthz`).then(r => r.ok, () => false)
    if (ok) break
    if (Date.now() > deadline) {
      child.kill('SIGKILL')
      throw new Error(`the console didn't start:\n${out}`)
    }
    await new Promise(r => setTimeout(r, 100))
  }
  return {
    url,
    output: () => out,
    stop: async () => {
      child.kill('SIGTERM')
      return exited
    },
  }
}

/** Runs the built server until it exits by itself; its exit code and output. */
export function runConsole(env: Record<string, string>): Promise<{ code: number | null, output: string }> {
  return new Promise((resolve) => {
    let output = ''
    const child = spawn(process.execPath, [join(root, '.output/server/index.mjs')], {
      env: { PATH: process.env.PATH ?? '', HOST: '127.0.0.1', PORT: '0', ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    child.stdout.on('data', c => (output += c))
    child.stderr.on('data', c => (output += c))
    const timer = setTimeout(() => child.kill('SIGKILL'), 8000)
    child.on('exit', (code) => {
      clearTimeout(timer)
      resolve({ code, output })
    })
  })
}

/** A browser, as far as cookies and redirects go. */
export class Browser {
  cookies = new Map<string, string>()
  constructor(public origin: string) {}

  header(): string {
    return [...this.cookies].map(([k, v]) => `${k}=${v}`).join('; ')
  }

  /** One request, no redirects followed. Set-Cookie is applied to the jar. */
  async request(path: string, init: { method?: string, form?: Record<string, string>, origin?: string | null, cookie?: string } = {}): Promise<Response> {
    const headers: Record<string, string> = {}
    const cookie = init.cookie ?? this.header()
    if (cookie) headers.cookie = cookie
    let body: string | undefined
    if (init.form) {
      headers['content-type'] = 'application/x-www-form-urlencoded'
      body = new URLSearchParams(init.form).toString()
    }
    if (init.method && init.method !== 'GET' && init.origin !== null) headers.origin = init.origin ?? this.origin
    const res = await fetch(path.startsWith('http') ? path : this.origin + path, { method: init.method ?? 'GET', headers, body, redirect: 'manual' })
    if (init.cookie === undefined) this.take(res)
    return res
  }

  take(res: Response) {
    for (const c of res.headers.getSetCookie()) {
      const [pair, ...attrs] = c.split(';')
      const i = pair!.indexOf('=')
      const name = pair!.slice(0, i)
      const value = pair!.slice(i + 1)
      const gone = !value || attrs.some(a => /^\s*max-age=0$/i.test(a)) || attrs.some(a => /^\s*expires=Thu, 01 Jan 1970/i.test(a))
      if (gone) this.cookies.delete(name)
      else this.cookies.set(name, value)
    }
  }

  /** Logs in the way a person does: the form, the provider, the callback. Returns the last response. */
  async login(next = '/', follow = true): Promise<Response> {
    const start = await this.request('/auth/login', { method: 'POST', form: { next } })
    if (!follow || start.status !== 303) return start
    const atIdp = await fetch(start.headers.get('location')!, { redirect: 'manual' })
    const back = atIdp.headers.get('location')
    if (!back) return atIdp
    return this.request(back)
  }
}
