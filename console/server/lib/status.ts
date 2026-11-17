// Turning what the cluster holds into what the console shows. This is
// mcloud's own logic, ported: internal/kube/status.go and
// status_resources.go (what's read and how it's judged) and
// internal/cli/output.go (the JSON shape). Keep them in step: a change
// there belongs here too, and test/status.test.ts holds both to the same
// answers.
//
// Everything here is a pure function of Kubernetes objects and a clock.
import type { App, AppSummary, Bucket, Cond, Database, Domain, Health, Quota, Restricted, Revision, Service, Status, Workload } from '../../shared/types'

type Obj = Record<string, any>

const SYNC_ERROR = 'maxcloud.gronblom.dev/sync-error'
const SYNCED_REVISION = 'maxcloud.gronblom.dev/synced-revision'
/** On objects Flux applied from git: entries registered in platform/. */
const FLUX_OWNER_LABEL = 'kustomize.toolkit.fluxcd.io/name'
const ENVIRONMENT_OF = 'run.gronblom.dev/environment-of'
const DEFAULT_PATH = 'deploy/k8s'
const DEFAULT_BRANCH = 'main'

/** One of an object's status conditions; empty if it has none yet. */
export function condition(o: Obj | null | undefined, type: string): Cond {
  const conds = o?.status?.conditions
  if (Array.isArray(conds)) {
    for (const c of conds) {
      if (c && c.type === type) {
        const out: Cond = { status: typeof c.status === 'string' ? c.status : '' }
        if (typeof c.reason === 'string' && c.reason) out.reason = c.reason
        if (typeof c.message === 'string' && c.message) out.message = c.message
        return out
      }
    }
  }
  return { status: '' }
}

const isReady = (c: Cond) => c.status === 'True'

const str = (v: unknown): string => (v === undefined || v === null ? '' : String(v))

/** An Authelia username, as registry-admission.yaml and internal/registry check it. */
const OWNER_RE = /^[A-Za-z0-9]([A-Za-z0-9._@-]*[A-Za-z0-9])?$/

/** The entry's owners; a value that can't be read counts as none. */
export function parseOwners(s: string): string[] {
  const out: string[] = []
  for (const part of s.split(',')) {
    const o = part.trim()
    if (!o) continue
    if (!OWNER_RE.test(o) || o.length > 64) return []
    if (!out.includes(o)) out.push(o)
  }
  return out
}

/**
 * A registered app, from its registry entry (a ResourceSetInputProvider)
 * and its Flux Kustomization, if there is one.
 */
export function appOf(provider: Obj | null, kustomization: Obj | null, fallbackName = ''): App {
  const values: Obj = provider?.spec?.defaultValues ?? {}
  const v = (k: string) => str(values[k])
  const app: App = {
    name: (provider && v('name')) || provider?.metadata?.name || fallbackName,
    mode: '',
    owners: [],
    registeredIn: provider?.metadata?.labels?.[FLUX_OWNER_LABEL] ? 'git' : 'cluster',
  }
  if (provider) {
    if (v('deploy') === 'direct') {
      app.mode = 'direct'
    } else {
      app.mode = 'gitops'
      if (v('repo')) app.repo = v('repo')
      app.path = v('path') || DEFAULT_PATH
      app.branch = v('branch') || DEFAULT_BRANCH
    }
    app.owners = parseOwners(v('owners'))
    const annotations: Obj = provider.metadata?.annotations ?? {}
    const syncError = str(annotations[SYNC_ERROR])
    const syncedRevision = str(annotations[SYNCED_REVISION])
    if (syncError || syncedRevision) {
      app.sync = {}
      if (syncedRevision) app.sync.revision = syncedRevision
      if (syncError) app.sync.error = syncError
    }
    const envOf = str(provider.metadata?.labels?.[ENVIRONMENT_OF])
    if (envOf) app.environmentOf = envOf
  }
  if (kustomization) {
    app.deploy = { ...condition(kustomization, 'Ready') }
    const rev = str(kustomization.status?.lastAppliedRevision)
    if (rev) app.deploy.revision = rev
  }
  return app
}

/**
 * How an app is doing, from what could be read. A failure anywhere wins
 * over something still rolling out, which wins over all being well.
 */
export function healthOf(app: App, services: Cond[] | null, extra: { progressing?: boolean } = {}): Health {
  let failing = !!app.sync?.error
  let progressing = !!extra.progressing
  let known = false
  if (app.mode === 'gitops') {
    if (!app.deploy) {
      // Registered, and Flux hasn't made its Kustomization yet.
      progressing = true
    } else if (app.deploy.status === 'False') {
      failing = true
    } else if (app.deploy.status !== 'True') {
      progressing = true
    } else {
      known = true
    }
  }
  if (services) {
    known = true
    for (const c of services) {
      if (c.status === 'False') failing = true
      else if (c.status !== 'True') progressing = true
    }
  }
  if (failing) return 'failing'
  if (progressing) return 'progressing'
  return known ? 'ok' : 'unknown'
}

/** The apps list: every entry, with its Kustomization and its services' readiness. */
export function summarize(providers: Obj[], kustomizations: Obj[] | null, services: Obj[] | null): AppSummary[] {
  const ks = new Map<string, Obj>()
  for (const k of kustomizations ?? []) ks.set(str(k.metadata?.name), k)
  const byNs = new Map<string, Cond[]>()
  for (const s of services ?? []) {
    const ns = str(s.metadata?.namespace)
    if (!byNs.has(ns)) byNs.set(ns, [])
    byNs.get(ns)!.push(condition(s, 'Ready'))
  }
  const out: AppSummary[] = []
  for (const p of providers) {
    const name = str(p.spec?.defaultValues?.name) || str(p.metadata?.name)
    const app = appOf(p, ks.get(name) ?? null)
    const conds = services ? (byNs.get(app.name) ?? []) : null
    out.push({
      ...app,
      health: healthOf(app, conds),
      services: conds ? { ready: conds.filter(isReady).length, total: conds.length } : null,
    })
  }
  out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  return out
}

/** Everything read for one app. null: this login may not read it. */
export interface StatusInput {
  name: string
  provider: Obj | null
  kustomization: Obj | null
  gitRepository: Obj | null
  /** The app's namespace exists. */
  namespace: boolean
  pods: Obj[] | null
  services: Obj[] | null
  deployments: Obj[] | null
  cronJobs: Obj[] | null
  /** KEDA's kinds are optional: null leaves them out, as mcloud does. */
  scaledJobs: Obj[] | null
  scaledObjects: Obj[] | null
  databases: Obj[] | null
  buckets: Obj[] | null
  domains: Obj[] | null
  quota: Obj | null
  warnings: Obj[] | null
  restricted: Restricted[]
}

const time = (v: unknown): number => {
  const t = typeof v === 'string' ? Date.parse(v) : NaN
  return Number.isNaN(t) ? 0 : t
}

/** The whole of `mcloud status --json` for one app. */
export function statusOf(input: StatusInput, now: number): Status {
  const app = appOf(input.provider, input.kustomization, input.name)
  const st: Status = {
    ...app,
    registered: !!input.provider,
    quota: [],
    services: [],
    workloads: [],
    databases: [],
    buckets: [],
    domains: [],
    problems: [],
    alerts: null,
    health: 'unknown',
    restricted: input.restricted,
  }
  if (input.provider && app.repo && input.gitRepository) {
    st.fetch = condition(input.gitRepository, 'Ready')
  }
  if (!input.namespace) {
    st.health = healthOf(app, null)
    return st
  }

  const running = new Map<string, number>()
  const podNames = new Set<string>()
  const readyPods = new Set<string>()
  for (const p of input.pods ?? []) {
    const name = str(p.metadata?.name)
    podNames.add(name)
    for (const c of p.status?.conditions ?? []) {
      if (c?.type === 'Ready' && c.status === 'True') readyPods.add(name)
    }
    const svc = str(p.metadata?.labels?.['serving.knative.dev/service'])
    if (svc && p.status?.phase === 'Running' && !p.metadata?.deletionTimestamp) {
      running.set(svc, (running.get(svc) ?? 0) + 1)
    }
    const problem = podProblem(p, now)
    if (problem) st.problems.push(problem)
  }

  for (const k of input.services ?? []) {
    const name = str(k.metadata?.name)
    st.services.push({
      name,
      url: str(k.status?.url),
      public: k.metadata?.labels?.['run.gronblom.dev/expose'] === 'public',
      ready: condition(k, 'Ready'),
      revision: str(k.status?.latestReadyRevisionName),
      pods: input.pods ? (running.get(name) ?? 0) : null,
    })
  }

  for (const d of input.deployments ?? []) {
    const want = typeof d.spec?.replicas === 'number' ? d.spec.replicas : 1
    const ready = typeof d.status?.readyReplicas === 'number' ? d.status.readyReplicas : 0
    st.workloads.push({ kind: 'Deployment', name: str(d.metadata?.name), state: `${ready}/${want} ready`, ready: ready >= want })
  }
  for (const cj of input.cronJobs ?? []) {
    let state = `schedule ${str(cj.spec?.schedule)}`
    if (cj.status?.lastScheduleTime) state += `, last ran ${ago(time(cj.status.lastScheduleTime), now)}`
    if (cj.spec?.suspend === true) state += ', suspended'
    st.workloads.push({ kind: 'CronJob', name: str(cj.metadata?.name), state, ready: true })
  }
  for (const [kind, list] of [['ScaledJob', input.scaledJobs], ['ScaledObject', input.scaledObjects]] as const) {
    for (const o of list ?? []) {
      const ready = condition(o, 'Ready')
      const active = condition(o, 'Active')
      let state = 'idle'
      if (isReady(active)) state = 'active'
      if (!isReady(ready)) state = `not ready: ${ready.message || ready.reason || ''}`
      st.workloads.push({ kind, name: str(o.metadata?.name), state, ready: isReady(ready) })
    }
  }

  for (const d of input.databases ?? []) {
    st.databases.push({
      name: str(d.metadata?.name),
      phase: str(d.status?.phase),
      instances: `${str(d.status?.readyInstances) || '0'}/${str(d.spec?.instances) || '0'}`,
      healthy: isReady(condition(d, 'Ready')),
    })
  }

  st.buckets = bucketsOf(input.buckets ?? [])
  st.domains = domainsOf(input.domains ?? [])

  if (input.quota) {
    for (const r of ['requests.cpu', 'requests.memory', 'pods']) {
      const hard = input.quota.status?.hard?.[r]
      if (hard === undefined) continue
      st.quota.push({ resource: r, used: str(input.quota.status?.used?.[r]) || '0', hard: str(hard) })
    }
  }

  if (input.warnings) {
    // Warnings about pods that are gone are history, e.g. a Knative pod's
    // readiness failing while it drains on scale-down; failed probes of a
    // pod that's ready now were it starting up.
    const current = input.warnings.filter((e) => {
      if (e.involvedObject?.kind !== 'Pod') return true
      const pod = str(e.involvedObject?.name)
      return podNames.has(pod) && !(e.reason === 'Unhealthy' && readyPods.has(pod))
    })
    st.problems.push(...recentWarnings(current, 15 * 60_000, 5, now))
  }

  const settling = st.databases.some(d => !d.healthy) || st.workloads.some(w => !w.ready)
  st.health = healthOf(app, input.services ? st.services.map(s => s.ready) : null, { progressing: settling })
  return st
}

const observed = (o: Obj): boolean => {
  const g = o.status?.observedGeneration
  return typeof g === 'number' && g >= (typeof o.metadata?.generation === 'number' ? o.metadata.generation : 0)
}

const count = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null)

function bucketsOf(list: Obj[]): Bucket[] {
  const out = list.map((o): Bucket => {
    const b: Bucket = {
      name: str(o.metadata?.name),
      bucket: str(o.status?.bucketName),
      public: o.spec?.public === true,
      sizeBytes: count(o.status?.sizeBytes),
      objects: count(o.status?.objects),
      ready: condition(o, 'Ready'),
      observed: observed(o),
    }
    if (o.status?.url) b.url = str(o.status.url)
    return b
  })
  return out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
}

function domainsOf(list: Obj[]): Domain[] {
  const out = list.map((o): Domain => {
    const d: Domain = {
      host: str(o.metadata?.name),
      service: str(o.spec?.service),
      proxied: o.spec?.proxied === true,
      ready: condition(o, 'Ready'),
      observed: observed(o),
    }
    if (o.status?.url) d.url = str(o.status.url)
    return d
  })
  return out.sort((a, b) => (a.host < b.host ? -1 : a.host > b.host ? 1 : 0))
}

/** Describes a pod that isn't well, or returns "". */
export function podProblem(p: Obj, now: number): string {
  const name = str(p.metadata?.name)
  const phase = p.status?.phase
  if (phase === 'Succeeded' || p.metadata?.deletionTimestamp) return ''
  const statuses: Obj[] = [...(p.status?.initContainerStatuses ?? []), ...(p.status?.containerStatuses ?? [])]
  for (const cs of statuses) {
    const w = cs.state?.waiting
    if (w && w.reason && w.reason !== 'PodInitializing' && w.reason !== 'ContainerCreating') {
      return `pod ${name}: ${str(cs.name)} ${w.reason}: ${oneLine(str(w.message))}`
    }
    const t = cs.lastState?.terminated
    if (t && cs.restartCount > 0 && now - time(t.finishedAt) < 3600_000) {
      return `pod ${name}: ${str(cs.name)} restarted ${cs.restartCount} times, last exit ${t.exitCode} (${str(t.reason)}) ${ago(time(t.finishedAt), now)}`
    }
  }
  if (phase === 'Failed') {
    return `pod ${name}: failed: ${oneLine(str(p.status?.message)) || str(p.status?.reason)}`
  }
  if (phase === 'Pending' && now - time(p.metadata?.creationTimestamp) > 60_000) {
    for (const c of p.status?.conditions ?? []) {
      if (c?.type === 'PodScheduled' && c.status !== 'True') {
        return `pod ${name}: not scheduled: ${oneLine(str(c.message))}`
      }
    }
    return `pod ${name}: pending for ${ago(time(p.metadata?.creationTimestamp), now)}`
  }
  return ''
}

/** The newest distinct Warning events. */
export function recentWarnings(events: Obj[], withinMs: number, max: number, now: number): string[] {
  const last = (e: Obj): number => {
    for (const t of [e.series?.lastObservedTime, e.lastTimestamp, e.eventTime, e.metadata?.creationTimestamp]) {
      const v = time(t)
      if (v) return v
    }
    return 0
  }
  const sorted = [...events].sort((a, b) => last(b) - last(a))
  const seen = new Set<string>()
  const out: string[] = []
  for (const e of sorted) {
    if (now - last(e) > withinMs || out.length >= max) break
    const kind = str(e.involvedObject?.kind)
    const name = str(e.involvedObject?.name)
    const key = `${kind}/${name}/${str(e.reason)}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(`${ago(last(e), now)} ${kind.toLowerCase()}/${name} ${str(e.reason)}: ${oneLine(str(e.message))}`)
  }
  return out
}

export function oneLine(s: string): string {
  s = s.split(/\s+/).filter(Boolean).join(' ')
  return s.length > 160 ? `${s.slice(0, 157)}...` : s
}

/** "42s ago", "5m ago", "3h ago", "4d ago": as mcloud says it. */
export function ago(t: number, now: number): string {
  const s = Math.round((now - t) / 1000)
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 48 * 3600) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

/**
 * A service's past revisions, newest first: the deploy history the cluster
 * itself keeps. Only what's safe to show anyone who may see the app: a
 * revision's environment and the rest of its spec stay out.
 */
export function revisionsOf(services: Obj[], revisions: Obj[], now: number, max = 30): Revision[] {
  const traffic = new Map<string, number>()
  const latest = new Set<string>()
  for (const s of services) {
    if (s.status?.latestCreatedRevisionName) latest.add(str(s.status.latestCreatedRevisionName))
    for (const t of s.status?.traffic ?? []) {
      if (t?.revisionName && typeof t.percent === 'number') {
        traffic.set(t.revisionName, (traffic.get(t.revisionName) ?? 0) + t.percent)
      }
    }
  }
  const out = revisions.map((r): Revision => {
    const name = str(r.metadata?.name)
    const created = time(r.metadata?.creationTimestamp)
    return {
      service: str(r.metadata?.labels?.['serving.knative.dev/service']),
      name,
      created: created ? new Date(created).toISOString() : '',
      age: created ? ago(created, now) : '',
      ready: condition(r, 'Ready'),
      image: shortImage(str(r.status?.containerStatuses?.[0]?.imageDigest) || str(r.spec?.containers?.[0]?.image)),
      traffic: traffic.get(name) ?? 0,
      latest: latest.has(name),
    }
  })
  out.sort((a, b) => (a.created < b.created ? 1 : a.created > b.created ? -1 : 0))
  return out.slice(0, max)
}

/** registry.gronblom.dev/shop/shop@sha256:3f2a9c1e… becomes shop/shop@3f2a9c1e5b7d. */
export function shortImage(image: string): string {
  const [ref, digest] = image.split('@sha256:')
  const name = (ref ?? '').replace(/^registry\.gronblom\.dev\//, '')
  return digest ? `${name}@${digest.slice(0, 12)}` : name
}
