// The console's configuration, read from the environment once at start.
// Nothing here reaches the browser. docs/console.md lists the variables.

export interface Config {
  /** Fixtures instead of Authelia, the cluster and the apps API. */
  mock: boolean
  /** The console's own origin, e.g. https://console.makiya.dev. */
  origin: string
  /** Cookies are Secure and __Host- prefixed (every https origin). */
  secureCookies: boolean
  /** Cookie-sealing keys, 32 bytes each: the first seals, all of them open. */
  sessionKeys: Buffer[]
  oidc: {
    issuer: string
    clientId: string
    /** origin + the callback path; has to be one of the client's redirect_uris. */
    redirectUri: string
  }
  kube: {
    /** The API server. In a pod: https://kubernetes.default.svc. */
    url: string
    /** PEM file with the CA that signed its certificate; "" for the system's. */
    caFile: string
  }
  /** The platform's apps API, ending in /api/v1/apps/. */
  appsApi: string
  /** What the API server puts before a person's username. */
  userPrefix: string
}

export class ConfigError extends Error {}

const LOOPBACK = new Set(['localhost', '127.0.0.1', '[::1]'])

/** The URL's origin if it is https, or http on this machine (tests, a dev server). */
function httpsOrigin(name: string, value: string, problems: string[]): URL | null {
  let u: URL
  try {
    u = new URL(value)
  } catch {
    problems.push(`${name}: ${JSON.stringify(value)} isn't a URL`)
    return null
  }
  if (u.protocol !== 'https:' && !(u.protocol === 'http:' && LOOPBACK.has(u.hostname))) {
    problems.push(`${name}: must be https (http only on localhost)`)
    return null
  }
  if (u.username || u.password) {
    problems.push(`${name}: no credentials in the URL`)
    return null
  }
  return u
}

/** Reads one sealing key: base64 (or base64url) of exactly 32 bytes. */
export function parseKey(value: string): Buffer | null {
  const v = value.trim()
  if (!/^[A-Za-z0-9+/_-]+={0,2}$/.test(v)) return null
  const b = Buffer.from(v.replace(/-/g, '+').replace(/_/g, '/'), 'base64')
  return b.length === 32 ? b : null
}

/**
 * Reads the configuration, or throws a ConfigError naming everything that's
 * wrong: the server doesn't start with a missing or weak sealing key.
 */
export function loadConfig(env: Record<string, string | undefined> = process.env, randomKey?: () => Buffer): Config {
  const problems: string[] = []
  const get = (name: string, fallback = '') => (env[name] ?? '').trim() || fallback
  const mock = get('CONSOLE_MOCK') === '1'

  let originURL: URL | null = null
  const rawOrigin = get('CONSOLE_ORIGIN', mock ? 'http://localhost:3000' : '')
  if (!rawOrigin) {
    problems.push('CONSOLE_ORIGIN: not set. The console\'s own URL, e.g. https://console.makiya.dev')
  } else {
    originURL = httpsOrigin('CONSOLE_ORIGIN', rawOrigin, problems)
    if (originURL && (originURL.pathname !== '/' || originURL.search || originURL.hash)) {
      problems.push('CONSOLE_ORIGIN: an origin only, no path')
    }
  }
  const origin = originURL?.origin ?? ''

  const sessionKeys: Buffer[] = []
  const rawKeys = get('CONSOLE_SESSION_KEY')
  if (rawKeys) {
    for (const part of rawKeys.split(',')) {
      const key = parseKey(part)
      if (!key) {
        problems.push('CONSOLE_SESSION_KEY: each key is 32 random bytes in base64 (openssl rand -base64 32)')
        break
      }
      sessionKeys.push(key)
    }
  } else if (mock && randomKey) {
    // Mock mode only: sessions last until the server restarts.
    sessionKeys.push(randomKey())
  } else {
    problems.push('CONSOLE_SESSION_KEY: not set. The cookie-sealing key, 32 random bytes in base64 (openssl rand -base64 32)')
  }

  // Authelia's issuer has no trailing slash; tokens carry it as configured.
  const issuer = get('CONSOLE_OIDC_ISSUER', 'https://auth.lab.gronblom.dev').replace(/\/+$/, '')
  httpsOrigin('CONSOLE_OIDC_ISSUER', issuer, problems)
  const clientId = get('CONSOLE_OIDC_CLIENT_ID', 'kubernetes')
  const redirectPath = get('CONSOLE_OIDC_REDIRECT_PATH', '/auth/callback')
  if (redirectPath !== '/auth/callback' && redirectPath !== '/callback') {
    problems.push('CONSOLE_OIDC_REDIRECT_PATH: /auth/callback or /callback')
  }

  const kubeURL = httpsOrigin('CONSOLE_KUBE_URL', get('CONSOLE_KUBE_URL', 'https://kubernetes.default.svc'), problems)
  if (kubeURL && kubeURL.pathname !== '/') problems.push('CONSOLE_KUBE_URL: no path')

  const appsApiURL = httpsOrigin('CONSOLE_APPS_API', get('CONSOLE_APPS_API', 'https://maxcloud-api.gronblom.dev/api/v1/apps/'), problems)
  if (appsApiURL && (!appsApiURL.pathname.endsWith('/') || appsApiURL.search)) {
    problems.push('CONSOLE_APPS_API: must end in / (…/api/v1/apps/)')
  }

  const userPrefix = env.CONSOLE_USER_PREFIX ?? 'authelia:'

  if (problems.length) {
    throw new ConfigError(problems.join('\n'))
  }
  return {
    mock,
    origin,
    secureCookies: originURL!.protocol === 'https:',
    sessionKeys,
    oidc: {
      issuer,
      clientId,
      redirectUri: origin + redirectPath,
    },
    kube: { url: kubeURL!.origin, caFile: get('CONSOLE_KUBE_CA_FILE') },
    appsApi: appsApiURL!.href,
    userPrefix,
  }
}
