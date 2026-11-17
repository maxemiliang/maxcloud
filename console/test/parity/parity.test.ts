// The console and the CLI agree: for the same cluster, the console's
// answers are mcloud's own `status --json` and `apps list --json`, field
// for field (plus what the console adds).
//
// This builds mcloud from this repo and runs it against the mock's made-up
// cluster, served over HTTP as a Kubernetes API. It needs Go, so it isn't
// part of `npm test`: run `npm run test:parity` after changing
// internal/kube/status.go, internal/cli/output.go or server/lib/status.ts.
import { execFile, execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { appStatus, listApps } from '../../server/lib/apps'
import { KubeReader } from '../../server/lib/kube'
import { fixtures, mockToken, mockTransport } from '../../server/lib/mock'

const run = promisify(execFile)
const repo = join(dirname(fileURLToPath(import.meta.url)), '../../..')

let hasGo = true
try {
  execFileSync('go', ['version'], { stdio: 'ignore' })
} catch {
  hasGo = false
}

/** What a real API server says each list is: client-go wants to be told. */
const KINDS: Record<string, [string, string]> = {
  pods: ['v1', 'Pod'],
  events: ['v1', 'Event'],
  resourcequotas: ['v1', 'ResourceQuota'],
  namespaces: ['v1', 'Namespace'],
  deployments: ['apps/v1', 'Deployment'],
  cronjobs: ['batch/v1', 'CronJob'],
  services: ['serving.knative.dev/v1', 'Service'],
  revisions: ['serving.knative.dev/v1', 'Revision'],
  scaledjobs: ['keda.sh/v1alpha1', 'ScaledJob'],
  scaledobjects: ['keda.sh/v1alpha1', 'ScaledObject'],
  clusters: ['postgresql.cnpg.io/v1', 'Cluster'],
  buckets: ['maxcloud.gronblom.dev/v1alpha1', 'Bucket'],
  domains: ['maxcloud.gronblom.dev/v1alpha1', 'Domain'],
  resourcesetinputproviders: ['fluxcd.controlplane.io/v1', 'ResourceSetInputProvider'],
  kustomizations: ['kustomize.toolkit.fluxcd.io/v1', 'Kustomization'],
  gitrepositories: ['source.toolkit.fluxcd.io/v1', 'GitRepository'],
}

describe.skipIf(!hasGo)('the console agrees with mcloud', () => {
  let server: Server
  let dir: string
  let mcloud: string
  const asked: string[] = []
  const env = () => ({ ...process.env, MCLOUD_KUBECONFIG: join(dir, 'kubeconfig'), MCLOUD_PLATFORM_URL: 'off', MCLOUD_PLAIN: '1', NO_COLOR: '1' })

  beforeAll(async () => {
    dir = mkdtempSync(join(tmpdir(), 'console-parity-'))
    mcloud = join(dir, 'mcloud')
    await run('go', ['build', '-o', mcloud, './cmd/mcloud'], { cwd: repo })

    const transport = mockTransport()
    server = createServer(async (req, res) => {
      asked.push(`${req.method} ${req.url}`)
      // client-go keeps its token to itself over plain HTTP: everyone here is the admin.
      const token = mockToken('admin')
      const out = await transport({ method: req.method as 'GET', path: req.url ?? '/', token })
      const path = (req.url ?? '').split('?')[0]!.split('/')
      // .../<resource> is a list, .../<resource>/<name> one object.
      const last = path[path.length - 1]!
      const kind = KINDS[last] ?? KINDS[path[path.length - 2]!]
      let body = out.body
      if (out.status === 200 && kind && body) {
        body = KINDS[last]
          ? { ...body, apiVersion: kind[0], kind: `${kind[1]}List`, metadata: {} }
          : { apiVersion: kind[0], kind: kind[1], ...body }
      }
      res.writeHead(out.status, { 'content-type': 'application/json' })
      res.end(JSON.stringify(body ?? {}))
    })
    await new Promise<void>(r => server.listen(0, '127.0.0.1', r))
    const port = (server.address() as AddressInfo).port
    writeFileSync(join(dir, 'kubeconfig'), [
      'apiVersion: v1', 'kind: Config', 'current-context: mock',
      'clusters:', '  - name: mock', `    cluster: {server: "http://127.0.0.1:${port}"}`,
      'users:', '  - name: admin', `    user: {token: "${mockToken('admin')}"}`,
      'contexts:', '  - name: mock', '    context: {cluster: mock, user: admin}', '',
    ].join('\n'))
  })

  afterAll(() => {
    server?.close()
    if (dir) rmSync(dir, { recursive: true, force: true })
  })

  const cli = async (...args: string[]) => JSON.parse((await run(mcloud, args, { env: env() })).stdout)
  const admin = () => new KubeReader(mockTransport(), mockToken('admin'))

  /** The console's answer without what only the console has. */
  const asMcloud = (v: any): any => {
    if (Array.isArray(v)) return v.map(asMcloud)
    if (!v || typeof v !== 'object') return v
    const out: Record<string, unknown> = {}
    for (const [k, val] of Object.entries(v)) {
      if (['health', 'restricted', 'environmentOf', 'observed'].includes(k)) continue
      out[k] = asMcloud(val)
    }
    return out
  }

  for (const app of Object.keys(fixtures(Date.now()))) {
    it(`status ${app}`, async () => {
      const theirs = await cli('status', app, '--json')
      const ours = asMcloud(await appStatus(admin(), app, Date.now()))
      // JSON drops what's undefined, as the wire would.
      expect(JSON.parse(JSON.stringify(ours))).toEqual(theirs)
    })
  }

  it('apps list', async () => {
    const theirs = await cli('apps', 'list', '--json')
    const ours = (await listApps(admin())).apps.map(({ services: _services, ...app }) => asMcloud(app))
    expect(JSON.parse(JSON.stringify({ apps: ours }))).toEqual(theirs)
  })

  it('a name that isn\'t an app is an error for both', async () => {
    await expect(cli('status', 'nosuch', '--json')).rejects.toThrow(/no app nosuch/)
    await expect(appStatus(admin(), 'nosuch', Date.now())).rejects.toThrow(/no app nosuch/)
  })

  it('the console asks for nothing mcloud status doesn\'t', async () => {
    asked.length = 0
    await cli('status', 'shop', '--json')
    const theirs = new Set(asked.map(a => a.split('?')[0]))
    const ours: string[] = []
    const spy = mockTransport()
    await appStatus(new KubeReader(async (r) => { ours.push(`${r.method} ${r.path.split('?')[0]}`); return spy(r) }, mockToken('admin')), 'shop', Date.now())
    expect(ours.filter(p => !theirs.has(p))).toEqual([])
  })
})
