// Reading the cluster as the logged-in user: every request carries the
// user's own ID token, so the API server's RBAC decides what comes back,
// exactly as it does for mcloud. The pod's ServiceAccount is never used
// (the pod doesn't even mount its token).
//
// The console can only make the requests in ALLOWED below: a fixed set of
// reads. A path is built from one of those shapes and an app's name, which
// has to be a DNS label first; nothing a browser sends is ever used as a
// path or a URL. No Secrets, no pod logs, no exec, no writes.
import { readFileSync } from 'node:fs'
import { request as httpsRequest } from 'node:https'
import { request as httpRequest } from 'node:http'

/** An app's name, which is also its namespace: a DNS label. */
const NAME = '[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?'
const NAME_RE = new RegExp(`^${NAME}$`)

export function isAppName(s: unknown): s is string {
  return typeof s === 'string' && NAME_RE.test(s)
}

/** Where the app registry and Flux's objects live. */
export const FLUX_NS = 'flux-system'
export const APP_LABEL = 'run.gronblom.dev/app'
export const TENANT_LABEL = 'run.gronblom.dev/tenant'

const enc = encodeURIComponent

/**
 * Every request the console may make, by name. Each is a function of
 * nothing, or of an app name that isAppName has passed.
 */
export const paths = {
  whoami: () => '/apis/authentication.k8s.io/v1/selfsubjectreviews',
  providers: () => `/apis/fluxcd.controlplane.io/v1/namespaces/${FLUX_NS}/resourcesetinputproviders?labelSelector=${enc(`${APP_LABEL}=true`)}`,
  provider: (app: string) => `/apis/fluxcd.controlplane.io/v1/namespaces/${FLUX_NS}/resourcesetinputproviders/${app}`,
  kustomizations: () => `/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/${FLUX_NS}/kustomizations`,
  kustomization: (app: string) => `/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/${FLUX_NS}/kustomizations/${app}`,
  gitRepository: (app: string) => `/apis/source.toolkit.fluxcd.io/v1/namespaces/${FLUX_NS}/gitrepositories/${app}`,
  allServices: () => '/apis/serving.knative.dev/v1/services',
  namespace: (app: string) => `/api/v1/namespaces/${app}`,
  pods: (app: string) => `/api/v1/namespaces/${app}/pods`,
  services: (app: string) => `/apis/serving.knative.dev/v1/namespaces/${app}/services`,
  revisions: (app: string) => `/apis/serving.knative.dev/v1/namespaces/${app}/revisions`,
  deployments: (app: string) => `/apis/apps/v1/namespaces/${app}/deployments?labelSelector=${enc('!serving.knative.dev/service')}`,
  cronJobs: (app: string) => `/apis/batch/v1/namespaces/${app}/cronjobs`,
  scaledJobs: (app: string) => `/apis/keda.sh/v1alpha1/namespaces/${app}/scaledjobs`,
  scaledObjects: (app: string) => `/apis/keda.sh/v1alpha1/namespaces/${app}/scaledobjects`,
  databases: (app: string) => `/apis/postgresql.cnpg.io/v1/namespaces/${app}/clusters`,
  buckets: (app: string) => `/apis/maxcloud.gronblom.dev/v1alpha1/namespaces/${app}/buckets`,
  domains: (app: string) => `/apis/maxcloud.gronblom.dev/v1alpha1/namespaces/${app}/domains`,
  quota: (app: string) => `/api/v1/namespaces/${app}/resourcequotas/tenant`,
  warnings: (app: string) => `/api/v1/namespaces/${app}/events?fieldSelector=${enc('type=Warning')}&limit=500`,
} as const

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
const PLACEHOLDER = 'zzzappzzz'

/** The allowlist, made from `paths` itself: one exact pattern per request. */
const ALLOWED: { method: 'GET' | 'POST', re: RegExp }[] = Object.entries(paths).map(([name, build]) => ({
  method: name === 'whoami' ? 'POST' as const : 'GET' as const,
  re: new RegExp(`^${esc((build as (a: string) => string)(PLACEHOLDER)).replaceAll(PLACEHOLDER, NAME)}$`),
}))

/** True only for a request the console is meant to make. */
export function isAllowed(method: string, path: string): boolean {
  return ALLOWED.some(a => a.method === method && a.re.test(path))
}

export interface KubeRequest {
  method: 'GET' | 'POST'
  path: string
  /** The user's ID token. */
  token: string
  body?: unknown
}
export interface KubeResponse { status: number, body: any }
/** How a request reaches the API server (tests and mock mode bring their own). */
export type KubeTransport = (req: KubeRequest) => Promise<KubeResponse>

/** The API server couldn't be asked, or answered something unexpected. */
export class KubeError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
}

/** The API server didn't accept the ID token: the session is no good. */
export class KubeUnauthorized extends KubeError {
  constructor() {
    super(401, 'the cluster didn\'t accept the login')
  }
}

const MAX_BODY = 32 << 20

/** Talks to the API server over TLS, trusting its CA (and nothing of the pod's own). */
export function httpsTransport(baseUrl: string, caFile: string): KubeTransport {
  const base = new URL(baseUrl)
  let ca: Buffer | undefined
  let caAt = 0
  const readCA = () => {
    // Read again every 10 minutes: the cluster's CA can be renewed.
    if (caFile && Date.now() - caAt > 600_000) {
      try {
        ca = readFileSync(caFile)
        caAt = Date.now()
      } catch (e) {
        // Keep the one we have; without any, nothing can be trusted.
        if (!ca) throw new KubeError(502, `the cluster's CA can't be read (${caFile}): ${(e as NodeJS.ErrnoException).code ?? 'error'}`)
      }
    }
    return ca
  }
  // Now, not at the first request: a wrong path stops the server at start.
  readCA()
  return req => new Promise<KubeResponse>((resolve, reject) => {
    const payload = req.body === undefined ? undefined : Buffer.from(JSON.stringify(req.body))
    const doRequest = base.protocol === 'https:' ? httpsRequest : httpRequest
    const r = doRequest({
      protocol: base.protocol,
      hostname: base.hostname,
      port: base.port || (base.protocol === 'https:' ? 443 : 80),
      method: req.method,
      path: req.path,
      ca: base.protocol === 'https:' ? readCA() : undefined,
      timeout: 15_000,
      headers: {
        'accept': 'application/json',
        'authorization': `Bearer ${req.token}`,
        'user-agent': 'maxcloud-console',
        ...(payload ? { 'content-type': 'application/json', 'content-length': payload.length } : {}),
      },
    }, (res) => {
      const chunks: Buffer[] = []
      let size = 0
      res.on('data', (c: Buffer) => {
        size += c.length
        if (size > MAX_BODY) {
          r.destroy(new Error('the answer is too large'))
          return
        }
        chunks.push(c)
      })
      res.on('end', () => {
        let body: any = null
        try {
          body = JSON.parse(Buffer.concat(chunks).toString('utf8'))
        } catch {
          // Not JSON (a proxy's error page): the status says enough.
        }
        resolve({ status: res.statusCode ?? 0, body })
      })
      res.on('error', reject)
    })
    r.on('timeout', () => r.destroy(new Error('timed out')))
    r.on('error', e => reject(new KubeError(502, `the cluster's API can't be reached: ${e.message}`)))
    r.end(payload)
  })
}

export type ListResult
  = | { ok: true, items: any[] }
    /** denied: this login may not read it. missing: the kind isn't installed. */
    | { ok: false, why: 'denied' | 'missing' }

export type GetResult
  = | { ok: true, object: any }
    | { ok: false, why: 'denied' | 'missing' }

/** Reads for one user. Every call goes through the allowlist. */
export class KubeReader {
  constructor(private transport: KubeTransport, private token: string) {}

  private async call(method: 'GET' | 'POST', path: string, body?: unknown): Promise<KubeResponse> {
    if (!isAllowed(method, path)) {
      // A bug in the console, not something a user can cause: fail loudly.
      throw new KubeError(500, `the console doesn't make this request: ${method} ${path.split('?')[0]}`)
    }
    const res = await this.transport({ method, path, token: this.token, body })
    if (res.status === 401) throw new KubeUnauthorized()
    return res
  }

  /** A list's items. 403 and 404 are answers, not errors: the caller says what they mean. */
  async list(path: string): Promise<ListResult> {
    const res = await this.call('GET', path)
    if (res.status === 200 && Array.isArray(res.body?.items)) return { ok: true, items: res.body.items }
    if (res.status === 403) return { ok: false, why: 'denied' }
    if (res.status === 404) return { ok: false, why: 'missing' }
    throw new KubeError(502, `the cluster answered HTTP ${res.status}`)
  }

  async get(path: string): Promise<GetResult> {
    const res = await this.call('GET', path)
    if (res.status === 200 && res.body && typeof res.body === 'object') return { ok: true, object: res.body }
    if (res.status === 403) return { ok: false, why: 'denied' }
    if (res.status === 404) return { ok: false, why: 'missing' }
    throw new KubeError(502, `the cluster answered HTTP ${res.status}`)
  }

  /**
   * Who the API server says this token is: its username and groups, as RBAC
   * sees them. Every authenticated user may ask (a SelfSubjectReview).
   */
  async whoami(): Promise<{ username: string, groups: string[] }> {
    const res = await this.call('POST', paths.whoami(), {
      apiVersion: 'authentication.k8s.io/v1',
      kind: 'SelfSubjectReview',
    })
    const info = res.body?.status?.userInfo
    if ((res.status !== 201 && res.status !== 200) || typeof info?.username !== 'string') {
      throw new KubeError(502, `the cluster didn't say who is logged in (HTTP ${res.status})`)
    }
    return { username: info.username, groups: Array.isArray(info.groups) ? info.groups.filter((g: unknown) => typeof g === 'string') : [] }
  }
}
