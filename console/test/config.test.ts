import { randomBytes } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { ConfigError, loadConfig, parseKey } from '../server/lib/config'

const KEY = randomBytes(32).toString('base64')
const real = { CONSOLE_ORIGIN: 'https://console.example.com', CONSOLE_SESSION_KEY: KEY }

describe('configuration', () => {
  it('reads the platform\'s defaults', () => {
    const c = loadConfig(real)
    expect(c.mock).toBe(false)
    expect(c.origin).toBe('https://console.example.com')
    expect(c.secureCookies).toBe(true)
    expect(c.sessionKeys).toHaveLength(1)
    expect(c.oidc).toEqual({
      issuer: 'https://auth.lab.gronblom.dev',
      clientId: 'kubernetes',
      redirectUri: 'https://console.example.com/auth/callback',
    })
    expect(c.kube).toEqual({ url: 'https://kubernetes.default.svc', caFile: '' })
    expect(c.appsApi).toBe('https://maxcloud-api.gronblom.dev/api/v1/apps/')
    expect(c.userPrefix).toBe('authelia:')
  })

  it('doesn\'t start without a sealing key', () => {
    expect(() => loadConfig({ CONSOLE_ORIGIN: 'https://console.example.com' })).toThrow(ConfigError)
    expect(() => loadConfig({ CONSOLE_ORIGIN: 'https://console.example.com' })).toThrow(/CONSOLE_SESSION_KEY/)
    // Not even with a generator at hand: that's for mock mode only.
    expect(() => loadConfig({ CONSOLE_ORIGIN: 'https://console.example.com' }, () => randomBytes(32))).toThrow(/CONSOLE_SESSION_KEY/)
  })

  it('doesn\'t start with a weak or unreadable key', () => {
    for (const bad of ['hunter2', randomBytes(16).toString('base64'), randomBytes(33).toString('base64'), 'not base64 !!', `${KEY},short`]) {
      expect(() => loadConfig({ ...real, CONSOLE_SESSION_KEY: bad }), bad).toThrow(/CONSOLE_SESSION_KEY/)
    }
  })

  it('takes several keys, for rotating them', () => {
    const second = randomBytes(32).toString('base64url')
    const c = loadConfig({ ...real, CONSOLE_SESSION_KEY: `${KEY}, ${second}` })
    expect(c.sessionKeys).toHaveLength(2)
  })

  it('doesn\'t start without its own origin', () => {
    expect(() => loadConfig({ CONSOLE_SESSION_KEY: KEY })).toThrow(/CONSOLE_ORIGIN/)
  })

  it('wants https, except on this machine', () => {
    expect(() => loadConfig({ ...real, CONSOLE_ORIGIN: 'http://console.example.com' })).toThrow(/https/)
    expect(() => loadConfig({ ...real, CONSOLE_OIDC_ISSUER: 'http://auth.example.com' })).toThrow(/CONSOLE_OIDC_ISSUER/)
    expect(() => loadConfig({ ...real, CONSOLE_KUBE_URL: 'http://10.0.0.1:6443' })).toThrow(/CONSOLE_KUBE_URL/)
    expect(() => loadConfig({ ...real, CONSOLE_APPS_API: 'http://platform.example.com/api/v1/apps/' })).toThrow(/CONSOLE_APPS_API/)
    const local = loadConfig({ ...real, CONSOLE_ORIGIN: 'http://127.0.0.1:18000', CONSOLE_OIDC_REDIRECT_PATH: '/callback' })
    expect(local.secureCookies).toBe(false)
    expect(local.oidc.redirectUri).toBe('http://127.0.0.1:18000/callback')
  })

  it('refuses an origin with a path, and URLs with credentials', () => {
    expect(() => loadConfig({ ...real, CONSOLE_ORIGIN: 'https://example.com/console' })).toThrow(/origin only/)
    expect(() => loadConfig({ ...real, CONSOLE_KUBE_URL: 'https://user:pw@kubernetes.default.svc' })).toThrow(/credentials/)
    expect(() => loadConfig({ ...real, CONSOLE_OIDC_REDIRECT_PATH: '/anything' })).toThrow(/REDIRECT_PATH/)
  })

  it('names everything that\'s wrong at once', () => {
    try {
      loadConfig({})
      expect.unreachable()
    } catch (e) {
      expect((e as Error).message).toMatch(/CONSOLE_ORIGIN/)
      expect((e as Error).message).toMatch(/CONSOLE_SESSION_KEY/)
    }
  })

  it('runs the mock without either, and only when asked', () => {
    const c = loadConfig({ CONSOLE_MOCK: '1' }, () => randomBytes(32))
    expect(c.mock).toBe(true)
    expect(c.sessionKeys).toHaveLength(1)
    expect(loadConfig({ ...real, CONSOLE_MOCK: 'true' }).mock).toBe(false)
    expect(loadConfig({ ...real, CONSOLE_MOCK: '0' }).mock).toBe(false)
  })

  it('reads a key as base64 or base64url', () => {
    const raw = randomBytes(32)
    expect(parseKey(raw.toString('base64'))).toEqual(raw)
    expect(parseKey(raw.toString('base64url'))).toEqual(raw)
    expect(parseKey('')).toBeNull()
  })
})
