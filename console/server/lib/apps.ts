// What the console reads for its pages: the requests (all from kube.ts's
// allowlist, all as the user) and what a refusal means for each.
import type { AppsResponse, Restricted, RevisionsResponse, Status } from '../../shared/types'
import { isAppName, paths, TENANT_LABEL, type KubeReader } from './kube'
import { revisionsOf, statusOf, summarize } from './status'

/** The page can't be shown: no such app, or this login may not see apps at all. */
export class NotAvailable extends Error {
  constructor(public status: 403 | 404, message: string) {
    super(message)
  }
}

const NOT_A_DEVELOPER = 'Your login can\'t read the app registry. An admin adds you to maxcloud-developers (platform/README.md, "Add a developer").'

/** The apps this login can see: every registered app, with its health. */
export async function listApps(kube: KubeReader): Promise<AppsResponse> {
  const [providers, kustomizations, services] = await Promise.all([
    kube.list(paths.providers()),
    kube.list(paths.kustomizations()),
    kube.list(paths.allServices()),
  ])
  if (!providers.ok) {
    if (providers.why === 'denied') throw new NotAvailable(403, NOT_A_DEVELOPER)
    // No ResourceSetInputProvider kind: not a maxcloud cluster.
    return { apps: [] }
  }
  return {
    apps: summarize(
      providers.items,
      kustomizations.ok ? kustomizations.items : null,
      services.ok ? services.items : null,
    ),
  }
}

/**
 * One app, as `mcloud status` collects it. Where mcloud stops at the first
 * refusal, the console shows what it may and says what's missing: a
 * developer who doesn't own the app sees its services, database and
 * buckets, not its pods, workloads, quota or domains.
 */
export async function appStatus(kube: KubeReader, name: string, now: number): Promise<Status> {
  if (!isAppName(name)) throw new NotAvailable(404, 'no such app')
  const [provider, kustomization, namespace] = await Promise.all([
    kube.get(paths.provider(name)),
    kube.get(paths.kustomization(name)),
    kube.get(paths.namespace(name)),
  ])
  if (!provider.ok && provider.why === 'denied') throw new NotAvailable(403, NOT_A_DEVELOPER)
  const entry = provider.ok ? provider.object : null

  // Only apps' namespaces: the console shows apps, whatever else a login
  // (an admin's) could read.
  const ns = namespace.ok ? namespace.object : null
  const tenant = ns?.metadata?.labels?.[TENANT_LABEL] === 'true'
  if (!entry && !tenant) throw new NotAvailable(404, `no app ${name} on the cluster`)

  const repo = entry?.spec?.defaultValues?.repo
  const gitRepository = entry && repo ? await kube.get(paths.gitRepository(name)) : null

  const base = {
    name,
    provider: entry,
    kustomization: kustomization.ok ? kustomization.object : null,
    gitRepository: gitRepository?.ok ? gitRepository.object : null,
  }
  if (!tenant) {
    return statusOf({
      ...base, namespace: false, restricted: [],
      pods: null, services: null, deployments: null, cronJobs: null, scaledJobs: null, scaledObjects: null,
      databases: null, buckets: null, domains: null, quota: null, warnings: null,
    }, now)
  }

  const [pods, services, deployments, cronJobs, scaledJobs, scaledObjects, databases, buckets, domains, quota, warnings] = await Promise.all([
    kube.list(paths.pods(name)),
    kube.list(paths.services(name)),
    kube.list(paths.deployments(name)),
    kube.list(paths.cronJobs(name)),
    kube.list(paths.scaledJobs(name)),
    kube.list(paths.scaledObjects(name)),
    kube.list(paths.databases(name)),
    kube.list(paths.buckets(name)),
    kube.list(paths.domains(name)),
    kube.get(paths.quota(name)),
    kube.list(paths.warnings(name)),
  ])

  const restricted: Restricted[] = []
  const denied = (r: { ok: boolean, why?: string }) => !r.ok && r.why === 'denied'
  if (denied(pods)) restricted.push('pods')
  if (denied(deployments) || denied(cronJobs)) restricted.push('workloads')
  if (denied(quota)) restricted.push('quota')
  if (denied(domains)) restricted.push('domains')
  // Without the pods, which warnings are about pods still running can't be
  // told, and the pods' own trouble isn't seen at all.
  if (denied(pods) || denied(warnings)) restricted.push('problems')

  const items = (r: { ok: true, items: any[] } | { ok: false }) => (r.ok ? r.items : null)
  return statusOf({
    ...base,
    namespace: true,
    pods: items(pods),
    services: items(services),
    deployments: items(deployments),
    cronJobs: items(cronJobs),
    scaledJobs: items(scaledJobs),
    scaledObjects: items(scaledObjects),
    databases: items(databases),
    buckets: items(buckets),
    domains: items(domains),
    quota: quota.ok ? quota.object : null,
    warnings: items(warnings),
    restricted,
  }, now)
}

/** The revisions Knative keeps for the app's services, newest first. */
export async function appRevisions(kube: KubeReader, name: string, now: number): Promise<RevisionsResponse> {
  if (!isAppName(name)) throw new NotAvailable(404, 'no such app')
  const namespace = await kube.get(paths.namespace(name))
  if (!namespace.ok || namespace.object?.metadata?.labels?.[TENANT_LABEL] !== 'true') {
    throw new NotAvailable(404, `no app ${name} on the cluster`)
  }
  const [services, revisions] = await Promise.all([
    kube.list(paths.services(name)),
    kube.list(paths.revisions(name)),
  ])
  if (!revisions.ok && revisions.why === 'denied') throw new NotAvailable(403, 'This login may not read the app\'s revisions.')
  return {
    app: name,
    revisions: revisionsOf(services.ok ? services.items : [], revisions.ok ? revisions.items : [], now),
  }
}
