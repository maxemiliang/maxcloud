// Mock mode (CONSOLE_MOCK=1, what `npm run dev` runs): a made-up cluster,
// so the console can be worked on and tested without one. It stands in at
// the lowest layer, as a transport that answers Kubernetes paths with
// Kubernetes objects and applies the platform's RBAC to a made-up user, so
// everything above it (the allowlist, the shaping, the pages) runs as it
// does for real. Names and people here are placeholders.
import type { AlertsResponse, LogsResponse, MetricsResponse, Panel } from '../../shared/types'
import type { Feature } from './appsapi'
import type { KubeRequest, KubeResponse, KubeTransport } from './kube'

type Obj = Record<string, any>

/** Who can log in to the mock: a developer who owns some of the apps, and an admin. */
export const MOCK_USERS: Record<string, { groups: string[], label: string }> = {
  alice: { groups: ['authelia:maxcloud-developers'], label: 'alice, a developer who owns shop and notes' },
  admin: { groups: ['authelia:admins'], label: 'admin, who sees every app' },
}

export const mockToken = (user: string) => `mock.${user}`
export const mockUser = (token: string) => (token.startsWith('mock.') ? token.slice(5) : '')

const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z')
const MIN = 60_000
const HOUR = 3600_000

const cond = (type: string, status: string, reason = '', message = '') => ({ type, status, reason, message })

interface AppFixture {
  provider: Obj
  kustomization?: Obj
  gitRepository?: Obj
  ns: Record<string, Obj[] | Obj>
}

function provider(name: string, values: Obj, opts: { git?: boolean, annotations?: Obj, labels?: Obj } = {}): Obj {
  return {
    apiVersion: 'fluxcd.controlplane.io/v1',
    kind: 'ResourceSetInputProvider',
    metadata: {
      name,
      namespace: 'flux-system',
      labels: {
        'run.gronblom.dev/app': 'true',
        ...(opts.git ? { 'kustomize.toolkit.fluxcd.io/name': 'apps' } : { 'app.kubernetes.io/managed-by': 'mcloud' }),
        ...opts.labels,
      },
      annotations: opts.annotations ?? {},
    },
    spec: { type: 'Static', defaultValues: { name, ...values } },
  }
}

function kustomization(name: string, ready: [string, string, string], revision: string): Obj {
  return {
    metadata: { name, namespace: 'flux-system' },
    status: { conditions: [cond('Ready', ...ready)], lastAppliedRevision: revision },
  }
}

function ksvc(ns: string, name: string, o: { public?: boolean, ready?: [string, string?, string?], revision: string, previous?: string }): Obj {
  const ready = o.ready ?? ['True']
  return {
    metadata: { name, namespace: ns, labels: o.public ? { 'run.gronblom.dev/expose': 'public' } : {} },
    status: {
      url: o.public ? `https://${name}--${ns}.makiya.dev` : `http://${name}.${ns}.svc.cluster.local`,
      conditions: [cond('Ready', ready[0], ready[1], ready[2])],
      latestReadyRevisionName: ready[0] === 'True' ? o.revision : (o.previous ?? ''),
      latestCreatedRevisionName: o.revision,
      traffic: [{ revisionName: ready[0] === 'True' ? o.revision : (o.previous ?? o.revision), percent: 100, latestRevision: ready[0] === 'True' }],
    },
  }
}

function revision(ns: string, service: string, name: string, createdMs: number, image: string, ready: [string, string?, string?] = ['True']): Obj {
  return {
    metadata: { name, namespace: ns, creationTimestamp: iso(createdMs), labels: { 'serving.knative.dev/service': service } },
    spec: { containers: [{ image, env: [{ name: 'NOT_SHOWN', value: 'a revision\'s environment never leaves the server' }] }] },
    status: { conditions: [cond('Ready', ready[0], ready[1], ready[2])], containerStatuses: [{ imageDigest: image }] },
  }
}

function pod(ns: string, name: string, service: string, createdMs: number, o: { waiting?: [string, string], ready?: boolean } = {}): Obj {
  const ready = o.ready ?? !o.waiting
  return {
    metadata: { name, namespace: ns, creationTimestamp: iso(createdMs), labels: service ? { 'serving.knative.dev/service': service } : {} },
    status: {
      phase: o.waiting ? 'Pending' : 'Running',
      conditions: [{ type: 'PodScheduled', status: 'True' }, { type: 'Ready', status: ready ? 'True' : 'False' }],
      containerStatuses: [{
        name: 'user-container',
        restartCount: 0,
        state: o.waiting ? { waiting: { reason: o.waiting[0], message: o.waiting[1] } } : { running: {} },
      }],
    },
  }
}

function quota(cpu: string, memory: string, pods: string): Obj {
  return {
    metadata: { name: 'tenant' },
    status: {
      hard: { 'requests.cpu': '200m', 'requests.memory': '1Gi', 'pods': '20' },
      used: { 'requests.cpu': cpu, 'requests.memory': memory, pods },
    },
  }
}

function bucket(app: string, name: string, o: { public?: boolean, size?: number, objects?: number } = {}): Obj {
  return {
    metadata: { name, generation: 1 },
    spec: { public: !!o.public },
    status: {
      bucketName: `mc-${app}--${name}`,
      observedGeneration: 1,
      conditions: [cond('Ready', 'True', 'Ready')],
      ...(o.public ? { url: `https://${name}--${app}.makiya.dev/` } : {}),
      ...(o.size !== undefined ? { sizeBytes: o.size, objects: o.objects ?? 0 } : {}),
    },
  }
}

const database = (phase = 'Cluster in healthy state', ready = 1): Obj => ({
  metadata: { name: 'db' },
  spec: { instances: 1 },
  status: { phase, readyInstances: ready, conditions: [cond('Ready', ready ? 'True' : 'False')] },
})

const SHA = {
  shop: '4f1c2d7e9a0b3c5d6e7f8091a2b3c4d5e6f70812',
  staging: '9be0417c2a6d5f3e8b1c0d9e7f6a5b4c3d2e1f00',
  blog: '7a3e9d10c4b25f68e1a0d7c6b5a4938271605f4e',
  wiki: 'c05d8e2f71a9b34c6d0e1f2a3b4c5d6e7f809a1b',
  demo: 'a5036a59e2d14c7b8f60312d4e5f6a7b8c9d0e1f',
}
const digest = (seed: string) => `sha256:${seed.repeat(8).slice(0, 64)}`
const REG = 'registry.gronblom.dev'

/** The made-up cluster, as of now. */
export function fixtures(now: number): Record<string, AppFixture> {
  return {
    'shop': {
      provider: provider('shop', {
        repo: 'example-org/shop', owners: 'alice,bob', buckets: 'uploads,assets:public', db: 'true',
        domains: 'shop.makiya.dev:web', environments: '{"staging":{"branch":"staging"}}',
      }, { annotations: { 'maxcloud.gronblom.dev/synced-revision': `main@sha1:${SHA.shop}` } }),
      kustomization: kustomization('shop', ['True', 'ReconciliationSucceeded', `Applied revision: main@sha1:${SHA.shop}`], `main@sha1:${SHA.shop}`),
      gitRepository: { metadata: { name: 'shop' }, status: { conditions: [cond('Ready', 'True', 'Succeeded', `stored artifact for revision 'main@sha1:${SHA.shop}'`)] } },
      ns: {
        services: [
          ksvc('shop', 'api', { revision: 'api-00017' }),
          ksvc('shop', 'web', { public: true, revision: 'web-00042' }),
        ],
        revisions: [
          revision('shop', 'web', 'web-00042', now - 3 * HOUR, `${REG}/shop/shop@${digest('4f1c2d7e')}`),
          revision('shop', 'web', 'web-00041', now - 2 * 24 * HOUR, `${REG}/shop/shop@${digest('0b93a6e1')}`),
          revision('shop', 'web', 'web-00040', now - 6 * 24 * HOUR, `${REG}/shop/shop@${digest('d27c51f8')}`),
          revision('shop', 'api', 'api-00017', now - 3 * HOUR, `${REG}/shop/shop@${digest('4f1c2d7e')}`),
          revision('shop', 'api', 'api-00016', now - 2 * 24 * HOUR, `${REG}/shop/shop@${digest('0b93a6e1')}`),
        ],
        pods: [
          pod('shop', 'web-00042-deployment-6c9f7d5b8-x2k4q', 'web', now - 40 * MIN),
          pod('shop', 'db-1', '', now - 9 * 24 * HOUR),
          pod('shop', 'emails-7d8c6b5f4-hn2vz', '', now - 12 * MIN),
        ],
        deployments: [{ metadata: { name: 'emails' }, spec: { replicas: 1 }, status: { readyReplicas: 1 } }],
        cronjobs: [{ metadata: { name: 'nightly' }, spec: { schedule: '0 3 * * *' }, status: { lastScheduleTime: iso(now - 9 * HOUR) } }],
        scaledjobs: [],
        scaledobjects: [{ metadata: { name: 'emails' }, status: { conditions: [cond('Ready', 'True'), cond('Active', 'True')] } }],
        clusters: [database()],
        buckets: [bucket('shop', 'uploads', { size: 1_288_490_188, objects: 3410 }), bucket('shop', 'assets', { public: true, size: 52_428_800, objects: 212 })],
        domains: [{
          metadata: { name: 'shop.makiya.dev', generation: 1 },
          spec: { service: 'web', proxied: true },
          status: { observedGeneration: 1, url: 'https://shop.makiya.dev/', conditions: [cond('Ready', 'True', 'Routed')] },
        }],
        quota: quota('60m', '288Mi', '3'),
        events: [],
      },
    },
    'shop--staging': {
      provider: provider('shop--staging', { repo: 'example-org/shop', branch: 'staging', owners: 'alice,bob' }, { labels: { 'run.gronblom.dev/environment-of': 'shop' } }),
      kustomization: kustomization('shop--staging', ['Unknown', 'Progressing', 'Running health checks for revision staging@sha1:' + SHA.staging + ' with a timeout of 5m0s'], `staging@sha1:${SHA.shop}`),
      gitRepository: { metadata: { name: 'shop--staging' }, status: { conditions: [cond('Ready', 'True', 'Succeeded')] } },
      ns: {
        services: [ksvc('shop--staging', 'web', { public: true, revision: 'web-00009', previous: 'web-00008', ready: ['Unknown', 'RevisionMissing', 'Configuration "web" is waiting for a Revision to become ready.'] })],
        revisions: [
          revision('shop--staging', 'web', 'web-00009', now - 40_000, `${REG}/shop/shop@${digest('9be0417c')}`, ['Unknown', 'Deploying', '']),
          revision('shop--staging', 'web', 'web-00008', now - 26 * HOUR, `${REG}/shop/shop@${digest('31f0a9cc')}`),
        ],
        pods: [pod('shop--staging', 'web-00009-deployment-5b7c9d6f4-q8w2e', 'web', now - 35_000, { ready: false })],
        deployments: [], cronjobs: [], scaledjobs: [], scaledobjects: [], clusters: [], buckets: [], domains: [],
        quota: quota('20m', '64Mi', '1'),
        events: [],
      },
    },
    'notes': {
      provider: provider('notes', { deploy: 'direct', owners: 'alice', db: 'true' }),
      ns: {
        services: [ksvc('notes', 'web', {
          public: true, revision: 'web-00007', previous: 'web-00006',
          ready: ['False', 'RevisionFailed', `Revision "web-00007" failed with message: Unable to fetch image "${REG}/notes/notes:d41d8cd9": failed to resolve image to digest: MANIFEST_UNKNOWN: manifest unknown.`],
        })],
        revisions: [
          revision('notes', 'web', 'web-00007', now - 18 * MIN, `${REG}/notes/notes:d41d8cd9`, ['False', 'ContainerMissing', 'Unable to fetch image: MANIFEST_UNKNOWN']),
          revision('notes', 'web', 'web-00006', now - 5 * 24 * HOUR, `${REG}/notes/notes@${digest('e3b0c442')}`),
        ],
        pods: [
          pod('notes', 'web-00006-deployment-7f9b6c5d8-m4n7p', 'web', now - 2 * HOUR),
          pod('notes', 'web-00007-deployment-58d6f7c9b-t6r3k', 'web', now - 17 * MIN, { waiting: ['ImagePullBackOff', `Back-off pulling image "${REG}/notes/notes:d41d8cd9"`] }),
          pod('notes', 'db-1', '', now - 30 * 24 * HOUR),
        ],
        deployments: [], cronjobs: [], scaledjobs: [], scaledobjects: [],
        clusters: [database()],
        buckets: [],
        domains: [],
        quota: quota('40m', '192Mi', '3'),
        events: [{
          metadata: { creationTimestamp: iso(now - 17 * MIN) },
          involvedObject: { kind: 'Pod', name: 'web-00007-deployment-58d6f7c9b-t6r3k' },
          reason: 'Failed', type: 'Warning', lastTimestamp: iso(now - 2 * MIN),
          message: `Failed to pull image "${REG}/notes/notes:d41d8cd9": rpc error: code = NotFound desc = failed to pull and unpack image: not found`,
        }],
      },
    },
    'blog': {
      provider: provider('blog', { repo: 'example-org/blog', owners: 'carol', buckets: 'media:public', db: 'true' }, { annotations: { 'maxcloud.gronblom.dev/synced-revision': `main@sha1:${SHA.blog}` } }),
      kustomization: kustomization('blog', ['True', 'ReconciliationSucceeded', `Applied revision: main@sha1:${SHA.blog}`], `main@sha1:${SHA.blog}`),
      gitRepository: { metadata: { name: 'blog' }, status: { conditions: [cond('Ready', 'True', 'Succeeded')] } },
      ns: {
        services: [ksvc('blog', 'web', { public: true, revision: 'web-00023' })],
        revisions: [
          revision('blog', 'web', 'web-00023', now - 4 * 24 * HOUR, `${REG}/blog/blog@${digest('7a3e9d10')}`),
          revision('blog', 'web', 'web-00022', now - 11 * 24 * HOUR, `${REG}/blog/blog@${digest('5cd2a1b7')}`),
        ],
        pods: [pod('blog', 'db-1', '', now - 20 * 24 * HOUR)],
        deployments: [], cronjobs: [], scaledjobs: [], scaledobjects: [],
        clusters: [database()],
        buckets: [bucket('blog', 'media', { public: true, size: 734_003_200, objects: 1890 })],
        domains: [],
        quota: quota('20m', '128Mi', '1'),
        events: [],
      },
    },
    'wiki': {
      provider: provider('wiki', { repo: 'example-org/wiki', owners: 'dave' }, {
        annotations: {
          'maxcloud.gronblom.dev/synced-revision': `main@sha1:${SHA.wiki}`,
          'maxcloud.gronblom.dev/sync-error': 'domains: wiki.gronblom.dev isn\'t one of the hostnames an admin allowed wiki',
        },
      }),
      kustomization: kustomization('wiki', ['True', 'ReconciliationSucceeded', `Applied revision: main@sha1:${SHA.wiki}`], `main@sha1:${SHA.wiki}`),
      gitRepository: { metadata: { name: 'wiki' }, status: { conditions: [cond('Ready', 'True', 'Succeeded')] } },
      ns: {
        services: [ksvc('wiki', 'web', { revision: 'web-00003' })],
        revisions: [revision('wiki', 'web', 'web-00003', now - 8 * 24 * HOUR, `${REG}/wiki/wiki@${digest('c05d8e2f')}`)],
        pods: [], deployments: [], cronjobs: [], scaledjobs: [], scaledobjects: [], clusters: [], buckets: [], domains: [],
        quota: quota('0', '0', '0'),
        events: [],
      },
    },
    'run-demo': {
      provider: provider('run-demo', { path: 'platform/apps/run-demo', buckets: 'smoke,cdn:public', db: 'true' }, { git: true }),
      kustomization: kustomization('run-demo', ['True', 'ReconciliationSucceeded', `Applied revision: main@sha1:${SHA.demo}`], `main@sha1:${SHA.demo}`),
      ns: {
        services: [ksvc('run-demo', 'hello', { public: true, revision: 'hello-00004' }), ksvc('run-demo', 'whoami', { revision: 'whoami-00004' })],
        revisions: [
          revision('run-demo', 'hello', 'hello-00004', now - 30 * 24 * HOUR, 'traefik/whoami:v1.12.0'),
          revision('run-demo', 'whoami', 'whoami-00004', now - 30 * 24 * HOUR, 'traefik/whoami:v1.12.0'),
        ],
        pods: [pod('run-demo', 'db-1', '', now - 30 * 24 * HOUR)],
        deployments: [{ metadata: { name: 'postgrest' }, spec: { replicas: 1 }, status: { readyReplicas: 1 } }],
        cronjobs: [],
        scaledjobs: [{ metadata: { name: 'smoke-queue' }, status: { conditions: [cond('Ready', 'True'), cond('Active', 'False')] } }],
        scaledobjects: [],
        clusters: [database()],
        buckets: [bucket('run-demo', 'smoke', { size: 2048, objects: 2 }), bucket('run-demo', 'cdn', { public: true, size: 0, objects: 0 })],
        domains: [{
          metadata: { name: 'run-demo.lab.gronblom.dev', generation: 1 },
          spec: { service: 'whoami' },
          status: { observedGeneration: 1, conditions: [cond('Ready', 'True', 'Routed')] },
        }],
        quota: quota('30m', '160Mi', '2'),
        events: [],
      },
    },
  }
}

const ownersOf = (f: AppFixture): string[] => String(f.provider.spec.defaultValues.owners ?? '').split(',').filter(Boolean)

/** May this made-up user act as an owner of the app (admin in its namespace)? */
function owns(user: string, f: AppFixture | undefined): boolean {
  if (!MOCK_USERS[user]) return false
  if (MOCK_USERS[user]!.groups.includes('authelia:admins')) return true
  return !!f && ownersOf(f).includes(user)
}

const forbidden = (what: string): KubeResponse => ({
  status: 403,
  body: { kind: 'Status', status: 'Failure', reason: 'Forbidden', code: 403, message: `${what} is forbidden` },
})
const notFound: KubeResponse = { status: 404, body: { kind: 'Status', status: 'Failure', reason: 'NotFound', code: 404 } }
const list = (items: Obj[]): KubeResponse => ({ status: 200, body: { kind: 'List', items } })

/** What maxcloud-developer-read lets every developer read in any namespace. */
const DEVELOPER_READ = new Set(['services', 'revisions', 'clusters', 'buckets', 'events'])

/**
 * The made-up API server: answers the console's reads as the platform's
 * RBAC would for the user in the token (platform/infrastructure/configs/
 * auth/developers.yaml, and the owners' bindings from the app registry).
 */
export function mockTransport(now: () => number = Date.now): KubeTransport {
  return async (req: KubeRequest): Promise<KubeResponse> => {
    const user = mockUser(req.token)
    if (!MOCK_USERS[user]) return { status: 401, body: { kind: 'Status', code: 401, reason: 'Unauthorized' } }
    const fx = fixtures(now())
    const path = req.path.split('?')[0]!

    if (req.method === 'POST' && path === '/apis/authentication.k8s.io/v1/selfsubjectreviews') {
      return { status: 201, body: { status: { userInfo: { username: `authelia:${user}`, groups: [...MOCK_USERS[user]!.groups, 'system:authenticated'] } } } }
    }
    if (req.method !== 'GET') return { status: 405, body: null }

    let m = path.match(/^\/apis\/[^/]+\/v1\/namespaces\/flux-system\/(resourcesetinputproviders|kustomizations|gitrepositories)(?:\/([^/]+))?$/)
    if (m) {
      const pick = (f: AppFixture) => (m![1] === 'resourcesetinputproviders' ? f.provider : m![1] === 'kustomizations' ? f.kustomization : f.gitRepository)
      if (m[2]) {
        const o = fx[m[2]] && pick(fx[m[2]]!)
        return o ? { status: 200, body: o } : notFound
      }
      return list(Object.values(fx).map(pick).filter((o): o is Obj => !!o))
    }
    if (path === '/apis/serving.knative.dev/v1/services') {
      return list(Object.values(fx).flatMap(f => f.ns.services as Obj[]))
    }
    m = path.match(/^\/api\/v1\/namespaces\/([^/]+)$/)
    if (m) {
      return fx[m[1]!]
        ? { status: 200, body: { metadata: { name: m[1], labels: { 'run.gronblom.dev/tenant': 'true', 'pod-security.kubernetes.io/enforce': 'restricted' } } } }
        : notFound
    }
    m = path.match(/^\/apis?\/(?:[^/]+\/)?v1(?:alpha1)?\/namespaces\/([^/]+)\/([a-z]+)(?:\/([^/]+))?$/)
    if (m) {
      const [, ns, resource, name] = m as unknown as [string, string, string, string | undefined]
      const f = fx[ns]
      if (!DEVELOPER_READ.has(resource) && !owns(user, f)) {
        return forbidden(`${resource} in the namespace "${ns}"`)
      }
      if (!f) return resource === 'resourcequotas' ? notFound : list([])
      if (resource === 'resourcequotas') return name === 'tenant' ? { status: 200, body: f.ns.quota } : notFound
      const items = f.ns[resource]
      return Array.isArray(items) ? list(items) : notFound
    }
    return notFound
  }
}

/** The made-up apps API: an app's owners and admins get an answer, others a refusal. */
export function mockAppsApi(now: number, token: string, app: string, feature: Feature, query: URLSearchParams): Panel<any> {
  const fx = fixtures(now)
  const f = fx[app]
  if (!f) return { ok: false, reason: 'not_offered', message: `The platform has no ${feature} for this app: no app ${app}.` }
  if (!owns(mockUser(token), f)) {
    return { ok: false, reason: 'forbidden', message: `Only the app's owners and admins see its ${feature}.` }
  }
  const services = (f.ns.services as Obj[]).map(s => String(s.metadata.name))
  if (feature === 'alerts') {
    const data: AlertsResponse = { app, alerts: [] }
    if (app === 'notes') {
      data.alerts.push({
        name: 'KnativeRevisionPodsPending',
        state: 'firing',
        severity: 'warning',
        summary: 'Pods of notes/web-00007 have been Pending for 10 minutes',
        description: 'The revision can\'t start its pods: a missing image, or no room in the app\'s quota.',
        activeAt: iso(now - 12 * MIN),
        value: '1',
        labels: { namespace: 'notes', revision_name: 'web-00007' },
      })
    }
    return { ok: true, data }
  }
  if (feature === 'metrics') {
    const window = query.get('window') ?? '1h'
    const hours = { '1h': 1, '6h': 6, '24h': 24, '7d': 168 }[window] ?? 1
    const data: MetricsResponse = {
      app, window, at: iso(now),
      services: services.map((name, i) => {
        const requests = Math.round((app === 'shop' ? 1840 : 96) * hours / (name === 'web' ? 1 : 4 + i))
        const errors = app === 'notes' ? Math.round(requests * 0.21) : Math.round(requests * 0.002)
        return {
          name, requests, requestsPerSecond: requests / (hours * 3600), errors,
          errorRatio: requests ? errors / requests : null,
          p95Seconds: app === 'notes' ? 1.84 : 0.112 + i * 0.03,
          pods: name === 'web' ? 1 : 0,
          podsWanted: name === 'web' ? 1 : 0,
        }
      }),
      queues: app === 'shop' ? [{ name: 'emails', kind: 'ScaledObject', backlog: 3 }] : [],
      jobs: { running: 0, failed: 0 },
    }
    return { ok: true, data }
  }
  const limit = Number(query.get('limit') ?? 500)
  const lines = app === 'notes'
    ? ['GET /notes 200 12ms', 'GET /notes/41 200 9ms', 'error: relation "tags" does not exist', 'POST /notes 500 31ms', 'GET /healthz 200 1ms']
    : ['GET / 200 18ms', 'GET /products 200 42ms', 'GET /products/127 200 23ms', 'POST /cart 201 57ms', 'queue emails: sent order-confirmation to a customer', 'GET /healthz 200 1ms']
  const podName = String(((f.ns.pods as Obj[])[0]?.metadata?.name) ?? `${services[0]}-00001-deployment-0`)
  const entries = Array.from({ length: Math.min(limit, 60) }, (_, i) => ({
    time: new Date(now - (60 - i) * 47_000).toISOString(),
    pod: podName,
    container: 'user-container',
    line: lines[i % lines.length]!,
  }))
  const data: LogsResponse = { app, start: iso(now - HOUR), end: iso(now), entries, limited: false }
  return { ok: true, data }
}
