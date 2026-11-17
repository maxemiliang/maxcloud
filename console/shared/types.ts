// What the console's API answers. The app and status shapes are mcloud's
// own (`mcloud apps list --json`, `mcloud status --json`; docs/mcloud.md,
// "JSON output", from internal/cli/output.go): the same field names with
// the same meaning, so the console and the CLI agree. What the console adds
// is marked "console only".

/** A status condition: True, False, Unknown, or "" before there is one. */
export interface Cond {
  status: string
  reason?: string
  message?: string
}

export interface Deploy extends Cond {
  revision?: string
}

/** How an app is doing at a glance (console only). */
export type Health
  /** Deployed and every service Ready. */
  = | 'ok'
    /** Something is being rolled out, or hasn't reported yet. */
    | 'progressing'
    /** A deploy or a service failed, or a pushed maxcloud.yaml was refused. */
    | 'failing'
    /** This login can't see enough to say. */
    | 'unknown'

export interface App {
  name: string
  /** gitops or direct; "" when not registered. */
  mode: string
  repo?: string
  path?: string
  branch?: string
  /** Empty: an admin's app. */
  owners: string[]
  /** cluster (mcloud), or git (the platform's own apps). */
  registeredIn: string
  /** Flux applying the app's manifests (GitOps apps). */
  deploy?: Deploy
  /** The app's pushed maxcloud.yaml, and whether the platform took it. */
  sync?: { revision?: string, error?: string }
  /** Console only: the app this is an environment or preview of. */
  environmentOf?: string
}

/** One row of the apps list: an app plus its health (console only). */
export interface AppSummary extends App {
  health: Health
  /** Knative services Ready, out of how many; null when they can't be read. */
  services: { ready: number, total: number } | null
}

export interface AppsResponse {
  apps: AppSummary[]
}

export interface Quota { resource: string, used: string, hard: string }

export interface Service {
  name: string
  url: string
  public: boolean
  ready: Cond
  revision: string
  /** Running pods; null when this login can't read the app's pods. */
  pods: number | null
}

export interface Workload { kind: string, name: string, state: string, ready: boolean }

export interface Database {
  name: string
  phase: string
  /** ready/wanted */
  instances: string
  healthy: boolean
}

export interface Bucket {
  name: string
  bucket: string
  public: boolean
  url?: string
  sizeBytes: number | null
  objects: number | null
  ready: Cond
  /** Console only: the controller has seen the bucket's current spec. */
  observed: boolean
}

export interface Domain {
  host: string
  service: string
  proxied: boolean
  url?: string
  ready: Cond
  /** Console only. */
  observed: boolean
}

/** Parts of an app only its owners and admins may read. */
export type Restricted = 'pods' | 'workloads' | 'quota' | 'domains' | 'problems'

export interface Status extends App {
  registered: boolean
  /** Flux fetching the app's repo. */
  fetch?: Cond
  quota: Quota[]
  services: Service[]
  workloads: Workload[]
  databases: Database[]
  buckets: Bucket[]
  domains: Domain[]
  problems: string[]
  /**
   * Always null here: the console asks the platform for alerts on their
   * own (/api/apps/<app>/alerts), so a slow answer doesn't hold the rest.
   */
  alerts: Alert[] | null
  /** Console only. */
  health: Health
  /**
   * Console only: what this login couldn't read. Empty for the app's
   * owners and admins; other developers see that an app exists and how its
   * services are, not its pods, workloads, quota or domains.
   */
  restricted: Restricted[]
}

/** One past revision of a Knative service (console only). */
export interface Revision {
  service: string
  name: string
  /** RFC 3339, UTC. */
  created: string
  /** e.g. "3h ago", worked out by the server. */
  age: string
  ready: Cond
  /** The image, without its registry's host when it's maxcloud's. */
  image: string
  /** Share of the service's traffic, 0 to 100. */
  traffic: number
  /** The service's newest revision. */
  latest: boolean
}

export interface RevisionsResponse {
  app: string
  revisions: Revision[]
}

// The platform's apps API (controller/internal/appapi), as mcloud reads it.

export interface LogEntry { time: string, pod: string, container: string, line: string }
export interface LogsResponse {
  app: string
  start: string
  end: string
  entries: LogEntry[]
  limited: boolean
}

export interface ServiceMetrics {
  name: string
  requests: number
  requestsPerSecond: number
  errors: number
  errorRatio: number | null
  p95Seconds: number | null
  pods: number | null
  podsWanted?: number | null
}
export interface MetricsResponse {
  app: string
  window: string
  at: string
  services: ServiceMetrics[]
  queues: { name: string, kind: string, backlog: number }[]
  jobs: { running: number, failed: number }
}

export interface Alert {
  name: string
  /** firing or pending */
  state: string
  severity?: string
  summary?: string
  description?: string
  activeAt?: string
  value?: string
  labels: Record<string, string>
}
export interface AlertsResponse {
  app: string
  alerts: Alert[]
}

/**
 * A panel that asks the platform: its data, or why there's none.
 * - forbidden: only the app's owners and admins may see it.
 * - not_offered: the platform doesn't serve it (yet), or not for this app.
 * - unavailable: it couldn't be reached, or failed.
 */
export type Panel<T>
  = | { ok: true, data: T }
    | { ok: false, reason: 'forbidden' | 'not_offered' | 'unavailable', message: string }

export interface Me {
  user: string
  /** The server runs on fixtures (npm run dev). */
  mock: boolean
}
