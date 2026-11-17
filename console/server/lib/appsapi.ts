// The platform's apps API: an app's logs, metrics and alerts, which the
// maxcloud controller serves to the app's owners and admins
// (controller/internal/appapi). The console asks with the user's own ID
// token, as mcloud does, so the controller decides who gets an answer.
//
// It can be missing (a platform that doesn't serve it yet), refuse (not an
// owner) or fail (Loki or Prometheus down). Each of those is an answer for
// one panel, never an error for the page.
import type { Panel } from '../../shared/types'
import { isAppName } from './kube'
import type { Fetch } from './oidc'

export type Feature = 'logs' | 'metrics' | 'alerts'

const NOUN: Record<Feature, string> = { logs: 'logs', metrics: 'metrics', alerts: 'alerts' }

/** The log ranges and metric windows the console offers. Nothing else is passed on. */
export const LOG_RANGES = ['15m', '1h', '6h', '24h', '7d'] as const
export const METRIC_WINDOWS = ['1h', '6h', '24h', '7d'] as const
export const LOG_LIMITS = [100, 500, 1000] as const

/** The query for a feature, from what the browser asked: only known values get through. */
export function panelQuery(feature: Feature, q: Record<string, unknown>): URLSearchParams {
  const out = new URLSearchParams()
  const one = (v: unknown) => (Array.isArray(v) ? v[0] : v)
  if (feature === 'logs') {
    const since = String(one(q.since) ?? '')
    out.set('since', (LOG_RANGES as readonly string[]).includes(since) ? since : '1h')
    const limit = Number(one(q.limit))
    out.set('limit', String((LOG_LIMITS as readonly number[]).includes(limit) ? limit : 500))
  }
  if (feature === 'metrics') {
    const window = String(one(q.window) ?? '')
    out.set('window', (METRIC_WINDOWS as readonly string[]).includes(window) ? window : '1h')
  }
  return out
}

/** The session's token wasn't accepted: the caller ends the session. */
export class AppsApiUnauthorized extends Error {}

export interface AppsApiOptions {
  /** …/api/v1/apps/ */
  base: string
  fetch?: Fetch
}

/** Asks the platform for one feature of one app. */
export async function appsApi<T>(opts: AppsApiOptions, token: string, app: string, feature: Feature, query: URLSearchParams): Promise<Panel<T>> {
  const noun = NOUN[feature]
  if (!isAppName(app)) return { ok: false, reason: 'not_offered', message: 'No such app.' }
  // The host and the path's shape are fixed here; the app's name is a DNS
  // label, and the query holds only what panelQuery let through.
  const url = `${opts.base}${app}/${feature}${query.size ? `?${query}` : ''}`
  let resp: Response
  try {
    resp = await (opts.fetch ?? fetch)(url, {
      headers: { accept: 'application/json', authorization: `Bearer ${token}` },
      redirect: 'error',
      signal: AbortSignal.timeout(20_000),
    })
  } catch {
    return { ok: false, reason: 'unavailable', message: `The platform couldn't be reached for the app's ${noun}. Try again in a moment.` }
  }
  const text = await resp.text().catch(() => '')
  let body: any = null
  try {
    body = JSON.parse(text)
  } catch {
    // Not JSON: a proxy's error page, or a platform without this API.
  }
  if (resp.status === 200 && body && typeof body === 'object') {
    return { ok: true, data: body as T }
  }
  const said = typeof body?.error === 'string' ? body.error.slice(0, 300) : ''
  switch (resp.status) {
    case 401:
      throw new AppsApiUnauthorized()
    case 403:
      return { ok: false, reason: 'forbidden', message: `Only the app's owners and admins see its ${noun}.` }
    case 404:
      // The controller's own 404 names the app; anything else is a platform
      // that doesn't route the API (or this part of it) yet.
      return said
        ? { ok: false, reason: 'not_offered', message: `The platform has no ${noun} for this app: ${said}.` }
        : { ok: false, reason: 'not_offered', message: `The platform doesn't serve apps' ${noun} yet.` }
    case 400:
      return { ok: false, reason: 'unavailable', message: `The platform refused the request for ${noun}${said ? `: ${said}` : ''}.` }
  }
  return {
    ok: false,
    reason: 'unavailable',
    message: `The platform couldn't read the app's ${noun} right now${said ? ` (${said})` : ` (HTTP ${resp.status})`}.`,
  }
}
