// The real transport to the API server: TLS, trusting the cluster's CA
// from a file and nothing else for that connection. Needs the openssl
// command to make a CA and a certificate for the test.
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer, type Server } from 'node:https'
import type { AddressInfo } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { httpsTransport, KubeError, KubeReader, paths } from '../server/lib/kube'

let hasOpenssl = true
try {
  execFileSync('openssl', ['version'], { stdio: 'ignore' })
} catch {
  hasOpenssl = false
}

describe.skipIf(!hasOpenssl)('talking to the API server', () => {
  let dir: string
  let server: Server
  let port: number
  const seen: { method?: string, url?: string, auth?: string, body: string }[] = []

  beforeAll(async () => {
    dir = mkdtempSync(join(tmpdir(), 'console-tls-'))
    const run = (...args: string[]) => execFileSync('openssl', args, { cwd: dir, stdio: 'ignore' })
    // A cluster CA, a serving certificate for "localhost" signed by it, and
    // another CA that signed nothing.
    run('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'ca.key', '-out', 'ca.crt', '-subj', '/CN=test-cluster-ca', '-days', '2')
    run('req', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'tls.key', '-out', 'tls.csr', '-subj', '/CN=kube-apiserver')
    writeFileSync(join(dir, 'san.ext'), 'subjectAltName=DNS:localhost\n')
    run('x509', '-req', '-in', 'tls.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-out', 'tls.crt', '-days', '2', '-extfile', 'san.ext')
    run('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'other.key', '-out', 'other.crt', '-subj', '/CN=another-ca', '-days', '2')
    server = createServer({ key: readFileSync(join(dir, 'tls.key')), cert: readFileSync(join(dir, 'tls.crt')) }, (req, res) => {
      let body = ''
      req.on('data', c => (body += c))
      req.on('end', () => {
        seen.push({ method: req.method, url: req.url, auth: req.headers.authorization, body })
        res.writeHead(req.url === '/api/v1/namespaces/shop/pods' ? 200 : 403, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ items: [{ metadata: { name: 'web-1' } }] }))
      })
    })
    await new Promise<void>(r => server.listen(0, '127.0.0.1', r))
    port = (server.address() as AddressInfo).port
  })

  afterAll(() => {
    server?.close()
    if (dir) rmSync(dir, { recursive: true, force: true })
  })

  it('reads with the cluster\'s CA and the user\'s token', async () => {
    const kube = new KubeReader(httpsTransport(`https://localhost:${port}`, join(dir, 'ca.crt')), 'the-users-id-token')
    expect(await kube.list(paths.pods('shop'))).toEqual({ ok: true, items: [{ metadata: { name: 'web-1' } }] })
    expect(await kube.list(paths.pods('blog'))).toEqual({ ok: false, why: 'denied' })
    expect(seen[0]).toMatchObject({ method: 'GET', url: '/api/v1/namespaces/shop/pods', auth: 'Bearer the-users-id-token', body: '' })
  })

  it('sends a review as JSON', async () => {
    seen.length = 0
    await httpsTransport(`https://localhost:${port}`, join(dir, 'ca.crt'))({ method: 'POST', path: paths.whoami(), token: 't', body: { kind: 'SelfSubjectReview' } })
    expect(seen[0]).toMatchObject({ method: 'POST', body: '{"kind":"SelfSubjectReview"}' })
  })

  it('doesn\'t talk to a server another CA vouches for, or one nobody does', async () => {
    seen.length = 0
    for (const ca of [join(dir, 'other.crt'), '']) {
      const kube = new KubeReader(httpsTransport(`https://localhost:${port}`, ca), 'the-users-id-token')
      await expect(kube.list(paths.pods('shop')), ca).rejects.toBeInstanceOf(KubeError)
    }
    // The token never left.
    expect(seen).toEqual([])
  })

  it('doesn\'t talk to a server whose certificate is for another name', async () => {
    seen.length = 0
    const kube = new KubeReader(httpsTransport(`https://127.0.0.1:${port}`, join(dir, 'ca.crt')), 't')
    await expect(kube.list(paths.pods('shop'))).rejects.toBeInstanceOf(KubeError)
    expect(seen).toEqual([])
  })

  it('says so when the API server can\'t be reached, or the CA file isn\'t there', async () => {
    await expect(new KubeReader(httpsTransport('https://127.0.0.1:1', ''), 't').list(paths.pods('shop'))).rejects.toMatchObject({ status: 502 })
    // Found out when the server starts, not at someone's first request.
    expect(() => httpsTransport(`https://localhost:${port}`, join(dir, 'missing.crt'))).toThrow(/CA can't be read/)
  })
})
