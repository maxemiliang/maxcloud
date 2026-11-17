import { describe, expect, it } from 'vitest'
import { appRevisions, appStatus, listApps, NotAvailable } from '../server/lib/apps'
import { KubeReader, type KubeRequest } from '../server/lib/kube'
import { fixtures, mockToken, mockTransport } from '../server/lib/mock'
import { ago, appOf, condition, healthOf, oneLine, parseOwners, podProblem, recentWarnings, revisionsOf, shortImage, statusOf, summarize, type StatusInput } from '../server/lib/status'

const NOW = Date.parse('2026-11-17T12:00:00Z')
const at = (msAgo: number) => new Date(NOW - msAgo).toISOString()
const MIN = 60_000
const HOUR = 3600_000

const as = (user: string) => new KubeReader(mockTransport(() => NOW), mockToken(user))

const empty: StatusInput = {
  name: 'shop', provider: null, kustomization: null, gitRepository: null, namespace: true, restricted: [],
  pods: [], services: [], deployments: [], cronJobs: [], scaledJobs: [], scaledObjects: [], databases: [], buckets: [], domains: [],
  quota: null, warnings: [],
}

describe('a registered app (mcloud apps list --json)', () => {
  const entry = (values: Record<string, unknown>, meta: Record<string, unknown> = {}) => ({
    metadata: { name: 'shop', ...meta }, spec: { defaultValues: { name: 'shop', ...values } },
  })

  it('fills in the registry\'s defaults for a GitOps app', () => {
    expect(appOf(entry({ repo: 'example-org/shop', owners: 'alice, bob' }), null)).toEqual({
      name: 'shop', mode: 'gitops', repo: 'example-org/shop', path: 'deploy/k8s', branch: 'main', owners: ['alice', 'bob'], registeredIn: 'cluster',
    })
  })

  it('leaves the source out for a direct app', () => {
    expect(appOf(entry({ deploy: 'direct', owners: 'alice', repo: 'example-org/shop' }), null)).toEqual({
      name: 'shop', mode: 'direct', owners: ['alice'], registeredIn: 'cluster',
    })
  })

  it('knows the platform\'s own apps: in git, no owners', () => {
    const a = appOf(entry({ path: 'platform/apps/run-demo' }, { labels: { 'kustomize.toolkit.fluxcd.io/name': 'apps' } }), null)
    expect(a).toMatchObject({ registeredIn: 'git', owners: [], mode: 'gitops', path: 'platform/apps/run-demo' })
    expect(a.repo).toBeUndefined()
  })

  it('reports the deploy and what became of a pushed maxcloud.yaml', () => {
    const a = appOf(
      entry({ repo: 'example-org/shop' }, { annotations: { 'maxcloud.gronblom.dev/sync-error': 'domains: not allowed', 'maxcloud.gronblom.dev/synced-revision': 'main@sha1:abc' } }),
      { status: { conditions: [{ type: 'Ready', status: 'False', reason: 'BuildFailed', message: 'kustomize build failed' }], lastAppliedRevision: 'main@sha1:def' } },
    )
    expect(a.deploy).toEqual({ status: 'False', reason: 'BuildFailed', message: 'kustomize build failed', revision: 'main@sha1:def' })
    expect(a.sync).toEqual({ revision: 'main@sha1:abc', error: 'domains: not allowed' })
  })

  it('has no deploy before Flux made the Kustomization, and no sync without annotations', () => {
    const a = appOf(entry({ repo: 'example-org/shop' }), null)
    expect(a.deploy).toBeUndefined()
    expect(a.sync).toBeUndefined()
  })

  it('counts an unreadable owners value as none', () => {
    expect(parseOwners('alice,bob')).toEqual(['alice', 'bob'])
    expect(parseOwners(' alice , alice ,')).toEqual(['alice'])
    expect(parseOwners('alice,<script>')).toEqual([])
    expect(parseOwners('')).toEqual([])
  })

  it('reads a condition, or says there is none yet', () => {
    expect(condition({ status: { conditions: [{ type: 'Ready', status: 'True', reason: '', message: '' }] } }, 'Ready')).toEqual({ status: 'True' })
    expect(condition({ status: {} }, 'Ready')).toEqual({ status: '' })
    expect(condition(null, 'Ready')).toEqual({ status: '' })
  })
})

describe('health', () => {
  const gitops = { name: 'a', mode: 'gitops', owners: [], registeredIn: 'cluster' }
  const T = { status: 'True' }
  it('is ok only when the deploy applied and every service is ready', () => {
    expect(healthOf({ ...gitops, deploy: T }, [T, T])).toBe('ok')
    expect(healthOf({ ...gitops, mode: 'direct' }, [T])).toBe('ok')
  })
  it('lets a failure win over a rollout', () => {
    expect(healthOf({ ...gitops, deploy: T }, [T, { status: 'Unknown' }])).toBe('progressing')
    expect(healthOf({ ...gitops, deploy: T }, [{ status: 'False' }, { status: 'Unknown' }])).toBe('failing')
    expect(healthOf({ ...gitops, deploy: { status: 'False' } }, [T])).toBe('failing')
    expect(healthOf({ ...gitops, deploy: T, sync: { error: 'refused' } }, [T])).toBe('failing')
    expect(healthOf({ ...gitops }, [])).toBe('progressing')
    expect(healthOf({ ...gitops, deploy: T }, [T], { progressing: true })).toBe('progressing')
  })
  it('says unknown when nothing could be read', () => {
    expect(healthOf({ ...gitops, mode: 'direct' }, null)).toBe('unknown')
    expect(healthOf({ ...gitops, deploy: T }, null)).toBe('ok')
  })
})

describe('one app (mcloud status --json)', () => {
  it('has mcloud\'s fields, with mcloud\'s names', async () => {
    const st = await appStatus(as('alice'), 'shop', NOW)
    // docs/mcloud.md, "JSON output": the status row, plus what the console adds.
    expect(Object.keys(st).sort()).toEqual([
      'alerts', 'branch', 'buckets', 'databases', 'deploy', 'domains', 'fetch', 'health', 'mode', 'name', 'owners', 'path', 'problems',
      'quota', 'registered', 'registeredIn', 'repo', 'restricted', 'services', 'sync', 'workloads',
    ])
    expect(Object.keys(st.services[0]!).sort()).toEqual(['name', 'pods', 'public', 'ready', 'revision', 'url'])
    expect(Object.keys(st.workloads[0]!).sort()).toEqual(['kind', 'name', 'ready', 'state'])
    expect(Object.keys(st.databases[0]!).sort()).toEqual(['healthy', 'instances', 'name', 'phase'])
    expect(Object.keys(st.buckets[0]!).sort()).toEqual(['bucket', 'name', 'objects', 'observed', 'public', 'ready', 'sizeBytes', 'url'])
    expect(Object.keys(st.domains[0]!).sort()).toEqual(['host', 'observed', 'proxied', 'ready', 'service', 'url'])
    expect(Object.keys(st.quota[0]!).sort()).toEqual(['hard', 'resource', 'used'])
    expect(st.alerts).toBeNull()
  })

  it('shows an owner everything', async () => {
    const st = await appStatus(as('alice'), 'shop', NOW)
    expect(st.restricted).toEqual([])
    expect(st.health).toBe('ok')
    expect(st.registered).toBe(true)
    expect(st.fetch).toMatchObject({ status: 'True' })
    expect(st.services).toEqual([
      { name: 'api', url: 'http://api.shop.svc.cluster.local', public: false, ready: { status: 'True' }, revision: 'api-00017', pods: 0 },
      { name: 'web', url: 'https://web--shop.makiya.dev', public: true, ready: { status: 'True' }, revision: 'web-00042', pods: 1 },
    ])
    expect(st.workloads).toEqual([
      { kind: 'Deployment', name: 'emails', state: '1/1 ready', ready: true },
      { kind: 'CronJob', name: 'nightly', state: 'schedule 0 3 * * *, last ran 9h ago', ready: true },
      { kind: 'ScaledObject', name: 'emails', state: 'active', ready: true },
    ])
    expect(st.databases).toEqual([{ name: 'db', phase: 'Cluster in healthy state', instances: '1/1', healthy: true }])
    expect(st.buckets.map(b => b.name)).toEqual(['assets', 'uploads'])
    expect(st.buckets[0]).toMatchObject({ bucket: 'mc-shop--assets', public: true, url: 'https://assets--shop.makiya.dev/', sizeBytes: 52_428_800, objects: 212, observed: true })
    expect(st.buckets[1]!.url).toBeUndefined()
    expect(st.domains).toEqual([{ host: 'shop.makiya.dev', service: 'web', proxied: true, url: 'https://shop.makiya.dev/', ready: { status: 'True', reason: 'Routed' }, observed: true }])
    expect(st.quota).toEqual([
      { resource: 'requests.cpu', used: '60m', hard: '200m' },
      { resource: 'requests.memory', used: '288Mi', hard: '1Gi' },
      { resource: 'pods', used: '3', hard: '20' },
    ])
    expect(st.problems).toEqual([])
  })

  it('shows another developer that the app exists and how its services are, and says what it left out', async () => {
    const st = await appStatus(as('alice'), 'blog', NOW)
    expect(st.restricted).toEqual(['pods', 'workloads', 'quota', 'domains', 'problems'])
    expect(st.owners).toEqual(['carol'])
    expect(st.services).toHaveLength(1)
    // The pods can't be counted, which isn't the same as none.
    expect(st.services[0]!.pods).toBeNull()
    expect(st.databases).toHaveLength(1)
    expect(st.buckets).toHaveLength(1)
    expect(st.workloads).toEqual([])
    expect(st.quota).toEqual([])
    expect(st.domains).toEqual([])
    expect(st.problems).toEqual([])
    expect(st.health).toBe('ok')
  })

  it('shows an admin everything, apps without owners too', async () => {
    expect((await appStatus(as('admin'), 'blog', NOW)).restricted).toEqual([])
    const demo = await appStatus(as('admin'), 'run-demo', NOW)
    expect(demo).toMatchObject({ registeredIn: 'git', owners: [], restricted: [] })
    expect(demo.fetch).toBeUndefined()
    expect(demo.workloads).toContainEqual({ kind: 'ScaledJob', name: 'smoke-queue', state: 'idle', ready: true })
    expect((await appStatus(as('alice'), 'run-demo', NOW)).restricted).toContain('pods')
  })

  it('reports a failed rollout the way mcloud does', async () => {
    const st = await appStatus(as('alice'), 'notes', NOW)
    expect(st.health).toBe('failing')
    expect(st.mode).toBe('direct')
    expect(st.deploy).toBeUndefined()
    expect(st.services[0]).toMatchObject({ ready: { status: 'False', reason: 'RevisionFailed' }, revision: 'web-00006', pods: 1 })
    expect(st.problems).toEqual([
      'pod web-00007-deployment-58d6f7c9b-t6r3k: user-container ImagePullBackOff: Back-off pulling image "registry.gronblom.dev/notes/notes:d41d8cd9"',
      '2m ago pod/web-00007-deployment-58d6f7c9b-t6r3k Failed: Failed to pull image "registry.gronblom.dev/notes/notes:d41d8cd9": rpc error: code = NotFound desc = failed to pull and unpack image: not found',
    ])
  })

  it('is rolling out while a new revision comes up', async () => {
    const st = await appStatus(as('alice'), 'shop--staging', NOW)
    expect(st.health).toBe('progressing')
    expect(st.environmentOf).toBe('shop')
    expect(st.branch).toBe('staging')
  })

  it('finds nothing for a name that isn\'t an app, whoever asks', async () => {
    for (const name of ['nosuch', 'kube-system', 'flux-system', '../shop', 'Shop', 'shop/secrets', '']) {
      await expect(appStatus(as('admin'), name, NOW), name).rejects.toBeInstanceOf(NotAvailable)
    }
  })

  it('only ever shows apps\' namespaces, even to an admin who could read others', async () => {
    const seen: string[] = []
    const transport = async (r: KubeRequest) => {
      seen.push(r.path)
      // A cluster where kube-system exists and this login may read all of it.
      if (r.path === '/api/v1/namespaces/kube-system') return { status: 200, body: { metadata: { name: 'kube-system', labels: {} } } }
      if (r.path.includes('/flux-system/')) return { status: 404, body: {} }
      return { status: 200, body: { items: [{ metadata: { name: 'x' } }] } }
    }
    await expect(appStatus(new KubeReader(transport, 't'), 'kube-system', NOW)).rejects.toMatchObject({ status: 404 })
    expect(seen.filter(p => p.startsWith('/api/v1/namespaces/kube-system/'))).toEqual([])
    await expect(appRevisions(new KubeReader(transport, 't'), 'kube-system', NOW)).rejects.toMatchObject({ status: 404 })
  })

  it('tells someone who isn\'t a developer so', async () => {
    const transport = async () => ({ status: 403, body: { kind: 'Status' } })
    await expect(listApps(new KubeReader(transport, 't'))).rejects.toMatchObject({ status: 403 })
    await expect(appStatus(new KubeReader(transport, 't'), 'shop', NOW)).rejects.toMatchObject({ status: 403 })
  })

  it('still shows a registered app whose namespace isn\'t there yet', () => {
    const st = statusOf({ ...empty, namespace: false, provider: { metadata: { name: 'shop' }, spec: { defaultValues: { name: 'shop', repo: 'example-org/shop' } } } }, NOW)
    expect(st).toMatchObject({ registered: true, services: [], health: 'progressing' })
  })

  it('counts running pods per service, not ones on their way out', () => {
    const p = (name: string, phase: string, extra: Record<string, unknown> = {}) => ({
      metadata: { name, labels: { 'serving.knative.dev/service': 'web' }, creationTimestamp: at(HOUR), ...extra }, status: { phase },
    })
    const st = statusOf({
      ...empty,
      services: [{ metadata: { name: 'web' }, status: {} }],
      pods: [p('a', 'Running'), p('b', 'Running'), p('c', 'Pending'), p('d', 'Running', { deletionTimestamp: at(1000) })],
    }, NOW)
    expect(st.services[0]!.pods).toBe(2)
    expect(st.services[0]!.ready).toEqual({ status: '' })
  })

  it('describes workloads as mcloud does', () => {
    const st = statusOf({
      ...empty,
      deployments: [
        { metadata: { name: 'worker' }, spec: { replicas: 3 }, status: { readyReplicas: 2 } },
        { metadata: { name: 'solo' }, spec: {}, status: {} },
      ],
      cronJobs: [
        { metadata: { name: 'never' }, spec: { schedule: '*/5 * * * *' }, status: {} },
        { metadata: { name: 'paused' }, spec: { schedule: '0 0 * * *', suspend: true }, status: { lastScheduleTime: at(3 * 24 * HOUR) } },
      ],
      scaledJobs: [{ metadata: { name: 'jobs' }, status: { conditions: [{ type: 'Ready', status: 'False', reason: 'TriggerError', message: 'connection refused' }] } }],
      scaledObjects: null,
    }, NOW)
    expect(st.workloads).toEqual([
      { kind: 'Deployment', name: 'worker', state: '2/3 ready', ready: false },
      { kind: 'Deployment', name: 'solo', state: '0/1 ready', ready: false },
      { kind: 'CronJob', name: 'never', state: 'schedule */5 * * * *', ready: true },
      { kind: 'CronJob', name: 'paused', state: 'schedule 0 0 * * *, last ran 3d ago, suspended', ready: true },
      { kind: 'ScaledJob', name: 'jobs', state: 'not ready: connection refused', ready: false },
    ])
    expect(st.health).toBe('progressing')
  })

  it('shows a bucket the controller hasn\'t caught up with as not observed, and unknown sizes as null', () => {
    const st = statusOf({
      ...empty,
      buckets: [{ metadata: { name: 'new', generation: 2 }, spec: {}, status: { observedGeneration: 1, conditions: [{ type: 'Ready', status: 'True' }] } }],
      domains: [{ metadata: { name: 'x.makiya.dev', generation: 1 }, spec: { service: 'web' }, status: {} }],
    }, NOW)
    expect(st.buckets[0]).toEqual({ name: 'new', bucket: '', public: false, sizeBytes: null, objects: null, ready: { status: 'True' }, observed: false })
    expect(st.domains[0]).toEqual({ host: 'x.makiya.dev', service: 'web', proxied: false, ready: { status: '' }, observed: false })
  })

  it('reports only the quota lines the namespace has', () => {
    const st = statusOf({ ...empty, quota: { status: { hard: { 'requests.cpu': '200m', 'services.loadbalancers': '0' }, used: {} } } }, NOW)
    expect(st.quota).toEqual([{ resource: 'requests.cpu', used: '0', hard: '200m' }])
  })
})

describe('problems', () => {
  const pod = (status: Record<string, unknown>, meta: Record<string, unknown> = {}) => ({ metadata: { name: 'web-1', creationTimestamp: at(10 * MIN), ...meta }, status })

  it('names a container that can\'t start', () => {
    expect(podProblem(pod({ phase: 'Pending', containerStatuses: [{ name: 'app', state: { waiting: { reason: 'CrashLoopBackOff', message: 'back-off 5m0s\nrestarting failed container' } } }] }), NOW))
      .toBe('pod web-1: app CrashLoopBackOff: back-off 5m0s restarting failed container')
    expect(podProblem(pod({ phase: 'Pending', initContainerStatuses: [{ name: 'init', state: { waiting: { reason: 'CreateContainerConfigError', message: 'secret "app-secrets" not found' } } }] }), NOW))
      .toBe('pod web-1: init CreateContainerConfigError: secret "app-secrets" not found')
  })

  it('doesn\'t mind a container still being created', () => {
    expect(podProblem(pod({ phase: 'Pending', containerStatuses: [{ name: 'app', state: { waiting: { reason: 'ContainerCreating' } } }] }, { creationTimestamp: at(20_000) }), NOW)).toBe('')
    expect(podProblem(pod({ phase: 'Pending', containerStatuses: [{ name: 'app', state: { waiting: { reason: 'PodInitializing' } } }] }, { creationTimestamp: at(20_000) }), NOW)).toBe('')
  })

  it('names a recent restart, not an old one', () => {
    const restarted = (finished: string) => pod({ phase: 'Running', containerStatuses: [{ name: 'app', restartCount: 4, state: { running: {} }, lastState: { terminated: { exitCode: 137, reason: 'OOMKilled', finishedAt: finished } } }] })
    expect(podProblem(restarted(at(5 * MIN)), NOW)).toBe('pod web-1: app restarted 4 times, last exit 137 (OOMKilled) 5m ago')
    expect(podProblem(restarted(at(2 * HOUR)), NOW)).toBe('')
  })

  it('names a failed pod and one that can\'t be scheduled', () => {
    expect(podProblem(pod({ phase: 'Failed', message: 'The node was low on resource: memory.', reason: 'Evicted' }), NOW)).toBe('pod web-1: failed: The node was low on resource: memory.')
    expect(podProblem(pod({ phase: 'Failed', reason: 'Evicted' }), NOW)).toBe('pod web-1: failed: Evicted')
    expect(podProblem(pod({ phase: 'Pending', conditions: [{ type: 'PodScheduled', status: 'False', message: '0/2 nodes are available: 2 Insufficient cpu.' }] }), NOW))
      .toBe('pod web-1: not scheduled: 0/2 nodes are available: 2 Insufficient cpu.')
    expect(podProblem(pod({ phase: 'Pending', conditions: [{ type: 'PodScheduled', status: 'True' }] }), NOW)).toBe('pod web-1: pending for 10m ago')
    expect(podProblem(pod({ phase: 'Pending' }, { creationTimestamp: at(30_000) }), NOW)).toBe('')
  })

  it('leaves finished and leaving pods alone', () => {
    expect(podProblem(pod({ phase: 'Succeeded', containerStatuses: [{ name: 'app', state: { waiting: { reason: 'X' } } }] }), NOW)).toBe('')
    expect(podProblem(pod({ phase: 'Failed' }, { deletionTimestamp: at(1000) }), NOW)).toBe('')
  })

  const event = (kind: string, name: string, reason: string, msAgo: number, message = 'm') => ({
    involvedObject: { kind, name }, reason, message, lastTimestamp: at(msAgo), metadata: { creationTimestamp: at(msAgo + HOUR) },
  })

  it('lists the newest distinct warnings from the last 15 minutes, five at most', () => {
    const out = recentWarnings([
      event('Pod', 'a', 'BackOff', 3 * MIN),
      event('Pod', 'a', 'BackOff', MIN, 'the newer one'),
      event('Pod', 'b', 'Failed', 20 * MIN),
      event('Service', 'web', 'InternalError', 30_000),
      ...Array.from({ length: 6 }, (_, i) => event('Pod', `p${i}`, 'Unhealthy', (5 + i) * MIN)),
    ], 15 * MIN, 5, NOW)
    expect(out).toEqual([
      '30s ago service/web InternalError: m',
      '1m ago pod/a BackOff: the newer one',
      '5m ago pod/p0 Unhealthy: m',
      '6m ago pod/p1 Unhealthy: m',
      '7m ago pod/p2 Unhealthy: m',
    ])
  })

  it('uses the newest of an event\'s times', () => {
    const e = { involvedObject: { kind: 'Pod', name: 'a' }, reason: 'R', message: 'm', lastTimestamp: null, eventTime: at(2 * MIN), series: { lastObservedTime: at(40_000) }, metadata: { creationTimestamp: at(HOUR) } }
    expect(recentWarnings([e], 15 * MIN, 5, NOW)).toEqual(['40s ago pod/a R: m'])
  })

  it('drops warnings about pods that are gone, and failed probes of pods that are ready now', () => {
    const st = statusOf({
      ...empty,
      pods: [
        { metadata: { name: 'ready', creationTimestamp: at(HOUR) }, status: { phase: 'Running', conditions: [{ type: 'Ready', status: 'True' }] } },
        { metadata: { name: 'starting', creationTimestamp: at(30_000) }, status: { phase: 'Running', conditions: [{ type: 'Ready', status: 'False' }] } },
      ],
      warnings: [
        event('Pod', 'gone', 'Unhealthy', MIN),
        event('Pod', 'ready', 'Unhealthy', MIN),
        event('Pod', 'ready', 'BackOff', 2 * MIN),
        event('Pod', 'starting', 'Unhealthy', 3 * MIN),
        event('ScaledObject', 'emails', 'KEDAScalerFailed', 4 * MIN),
      ],
    }, NOW)
    expect(st.problems).toEqual([
      '2m ago pod/ready BackOff: m',
      '3m ago pod/starting Unhealthy: m',
      '4m ago scaledobject/emails KEDAScalerFailed: m',
    ])
  })

  it('words times and long messages as mcloud does', () => {
    expect([ago(NOW - 59_400, NOW), ago(NOW - 61_000, NOW), ago(NOW - 59 * MIN, NOW), ago(NOW - 47 * HOUR, NOW), ago(NOW - 49 * HOUR, NOW)])
      .toEqual(['59s ago', '1m ago', '59m ago', '47h ago', '2d ago'])
    expect(oneLine('  a\n\tb   c ')).toBe('a b c')
    expect(oneLine('x'.repeat(200))).toBe(`${'x'.repeat(157)}...`)
  })
})

describe('the apps list', () => {
  it('shows a developer every app, with its health', async () => {
    const { apps } = await listApps(as('alice'))
    expect(apps.map(a => [a.name, a.health, a.services])).toEqual([
      ['blog', 'ok', { ready: 1, total: 1 }],
      ['notes', 'failing', { ready: 0, total: 1 }],
      ['run-demo', 'ok', { ready: 2, total: 2 }],
      ['shop', 'ok', { ready: 2, total: 2 }],
      ['shop--staging', 'progressing', { ready: 0, total: 1 }],
      ['wiki', 'failing', { ready: 1, total: 1 }],
    ])
    expect(apps.find(a => a.name === 'wiki')!.sync?.error).toContain('wiki.gronblom.dev')
    expect(apps.find(a => a.name === 'shop--staging')!.environmentOf).toBe('shop')
  })

  it('still lists the apps when services or deploys can\'t be read', () => {
    const fx = fixtures(NOW)
    const out = summarize([fx.shop!.provider, fx.notes!.provider], null, null)
    expect(out.map(a => [a.name, a.health, a.services, a.deploy])).toEqual([
      ['notes', 'unknown', null, undefined],
      ['shop', 'progressing', null, undefined],
    ])
  })
})

describe('revisions', () => {
  it('lists them newest first, with who has the traffic', async () => {
    const { revisions } = await appRevisions(as('alice'), 'notes', NOW)
    expect(revisions).toEqual([
      { service: 'web', name: 'web-00007', created: at(18 * MIN), age: '18m ago', ready: { status: 'False', reason: 'ContainerMissing', message: 'Unable to fetch image: MANIFEST_UNKNOWN' }, image: 'notes/notes:d41d8cd9', traffic: 0, latest: true },
      { service: 'web', name: 'web-00006', created: at(5 * 24 * HOUR), age: '5d ago', ready: { status: 'True' }, image: 'notes/notes@e3b0c442e3b0', traffic: 100, latest: false },
    ])
  })

  it('never passes on a revision\'s environment or the rest of its spec', async () => {
    // Every developer may read revisions, and a revision carries its
    // container's env. The fixtures put a marker there.
    const fx = fixtures(NOW)
    expect(JSON.stringify(fx.blog!.ns.revisions)).toContain('NOT_SHOWN')
    const out = JSON.stringify(await appRevisions(as('alice'), 'blog', NOW))
    expect(out).not.toContain('NOT_SHOWN')
    expect(out).not.toContain('env')
    expect(Object.keys(revisionsOf(fx.blog!.ns.services as any[], fx.blog!.ns.revisions as any[], NOW)[0]!).sort())
      .toEqual(['age', 'created', 'image', 'latest', 'name', 'ready', 'service', 'traffic'])
  })

  it('shortens images', () => {
    expect(shortImage('registry.gronblom.dev/shop/shop@sha256:4f1c2d7e9a0b3c5d6e7f')).toBe('shop/shop@4f1c2d7e9a0b')
    expect(shortImage('ghcr.io/example-org/tool:1.2.3')).toBe('ghcr.io/example-org/tool:1.2.3')
    expect(shortImage('')).toBe('')
  })
})
