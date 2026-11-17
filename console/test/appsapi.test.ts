import { describe, expect, it } from 'vitest'
import { appsApi, AppsApiUnauthorized, panelQuery } from '../server/lib/appsapi'

const BASE = 'https://platform.example.com/api/v1/apps/'

function api(status: number, body: string | object, contentType = 'application/json') {
  const seen: { url: string, init?: RequestInit }[] = []
  const fetch = (async (url: string, init?: RequestInit) => {
    seen.push({ url, init })
    return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status, headers: { 'content-type': contentType } })
  }) as unknown as typeof globalThis.fetch
  return { seen, opts: { base: BASE, fetch } }
}

describe('what a panel may ask for', () => {
  it('passes on known ranges and nothing else', () => {
    expect(panelQuery('logs', { since: '6h', limit: '1000' }).toString()).toBe('since=6h&limit=1000')
    expect(panelQuery('logs', {}).toString()).toBe('since=1h&limit=500')
    expect(panelQuery('logs', { since: '9999d', limit: '999999', pod: '.*', container: 'x', all: 'true', until: 'now', query: '{namespace="other"}' }).toString())
      .toBe('since=1h&limit=500')
    expect(panelQuery('logs', { since: ['7d', '1h'], limit: ['100'] }).toString()).toBe('since=7d&limit=100')
    expect(panelQuery('metrics', { window: '7d' }).toString()).toBe('window=7d')
    expect(panelQuery('metrics', { window: '15d&namespace=other' }).toString()).toBe('window=1h')
    expect(panelQuery('alerts', { anything: 'x' }).toString()).toBe('')
  })
})

describe('asking the platform', () => {
  it('asks for one app\'s feature with the user\'s token', async () => {
    const { seen, opts } = api(200, { app: 'shop', alerts: [] })
    const out = await appsApi(opts, 'the-users-token', 'shop', 'alerts', new URLSearchParams())
    expect(out).toEqual({ ok: true, data: { app: 'shop', alerts: [] } })
    expect(seen).toHaveLength(1)
    expect(seen[0]!.url).toBe('https://platform.example.com/api/v1/apps/shop/alerts')
    expect(new Headers(seen[0]!.init?.headers).get('authorization')).toBe('Bearer the-users-token')
    expect(seen[0]!.init?.redirect).toBe('error')
  })

  it('adds only the query it was given', async () => {
    const { seen, opts } = api(200, { app: 'shop', entries: [] })
    await appsApi(opts, 't', 'shop', 'logs', panelQuery('logs', { since: '24h' }))
    expect(seen[0]!.url).toBe('https://platform.example.com/api/v1/apps/shop/logs?since=24h&limit=500')
  })

  it('doesn\'t ask at all for something that isn\'t an app\'s name', async () => {
    const { seen, opts } = api(200, {})
    for (const bad of ['../../../.well-known/maxcloud.json', 'shop/logs?x', 'shop%2F..', 'Shop', '']) {
      const out = await appsApi(opts, 't', bad, 'logs', new URLSearchParams())
      expect(out.ok, bad).toBe(false)
    }
    expect(seen).toEqual([])
  })

  it('explains a refusal: owners and admins only', async () => {
    const out = await appsApi(api(403, { error: 'authelia:alice may not list pods in blog: only the app\'s owners and admins may' }).opts, 't', 'blog', 'metrics', new URLSearchParams())
    expect(out).toEqual({ ok: false, reason: 'forbidden', message: 'Only the app\'s owners and admins see its metrics.' })
  })

  it('explains a platform that doesn\'t serve the API yet', async () => {
    // Traefik's own 404: no route for /api/v1/apps/ on the platform's host.
    const out = await appsApi(api(404, '404 page not found\n', 'text/plain').opts, 't', 'shop', 'logs', new URLSearchParams())
    expect(out).toEqual({ ok: false, reason: 'not_offered', message: 'The platform doesn\'t serve apps\' logs yet.' })
  })

  it('passes on the controller\'s own 404', async () => {
    const out = await appsApi(api(404, { error: 'no app shop' }).opts, 't', 'shop', 'alerts', new URLSearchParams())
    expect(out).toMatchObject({ ok: false, reason: 'not_offered' })
    expect(!out.ok && out.message).toContain('no app shop')
  })

  it('explains Loki or Prometheus being down, an error page, and no answer at all', async () => {
    expect(await appsApi(api(502, { error: 'loki: connection refused' }).opts, 't', 'shop', 'logs', new URLSearchParams()))
      .toMatchObject({ ok: false, reason: 'unavailable' })
    expect(await appsApi(api(530, '<html>error</html>', 'text/html').opts, 't', 'shop', 'logs', new URLSearchParams()))
      .toMatchObject({ ok: false, reason: 'unavailable' })
    expect(await appsApi(api(200, '<html>a login page</html>', 'text/html').opts, 't', 'shop', 'logs', new URLSearchParams()))
      .toMatchObject({ ok: false, reason: 'unavailable' })
    const down = { base: BASE, fetch: (async () => { throw new TypeError('fetch failed') }) as unknown as typeof fetch }
    expect(await appsApi(down, 't', 'shop', 'logs', new URLSearchParams())).toMatchObject({ ok: false, reason: 'unavailable' })
  })

  it('ends the session when the platform doesn\'t accept the token', async () => {
    await expect(appsApi(api(401, { error: 'token expired' }).opts, 't', 'shop', 'logs', new URLSearchParams())).rejects.toBeInstanceOf(AppsApiUnauthorized)
  })
})
