import { describe, expect, it } from 'vitest'
import { isAllowed, isAppName, KubeError, KubeReader, KubeUnauthorized, paths, type KubeRequest } from '../server/lib/kube'

describe('app names', () => {
  it('are DNS labels', () => {
    for (const ok of ['shop', 'a', 'run-demo', 'shop--staging', 'shop--pr-42', 'x1', '0day', 'a'.repeat(63)]) {
      expect(isAppName(ok), ok).toBe(true)
    }
    for (const bad of [
      '', '-shop', 'shop-', 'Shop', 'shop.prod', 'shop/secrets', '../kube-system', 'shop%2Fsecrets', 'shop?watch=true', 'shop#x',
      'shop secrets', 'shop\n', 'a'.repeat(64), '*', '..', '.', undefined, null, 42, ['shop'], { name: 'shop' },
    ]) {
      expect(isAppName(bad), String(bad)).toBe(false)
    }
  })
})

describe('the allowlist', () => {
  it('lets through every request the console builds', () => {
    for (const [name, build] of Object.entries(paths)) {
      const path = (build as (a: string) => string)('shop--staging')
      expect(isAllowed(name === 'whoami' ? 'POST' : 'GET', path), name).toBe(true)
    }
  })

  it('is short enough to read: reads only, and one review', () => {
    const names = Object.keys(paths)
    expect(names).toHaveLength(20)
    const all = names.map(n => (paths as Record<string, (a: string) => string>)[n]!('shop')).join('\n')
    for (const never of ['secrets', 'configmaps', '/log', 'exec', 'portforward', 'proxy', 'serviceaccounts', 'token', 'watch']) {
      expect(all, never).not.toContain(never)
    }
  })

  it('refuses everything else', () => {
    const refused: [string, string][] = [
      // Secrets, config, logs, exec, tokens: not the console's to read.
      ['GET', '/api/v1/namespaces/shop/secrets'],
      ['GET', '/api/v1/namespaces/shop/secrets/app-secrets'],
      ['GET', '/api/v1/secrets'],
      ['GET', '/api/v1/namespaces/shop/configmaps'],
      ['GET', '/api/v1/namespaces/shop/pods/web-1/log'],
      ['GET', '/api/v1/namespaces/shop/pods/web-1'],
      ['GET', '/api/v1/namespaces/shop/pods/web-1/exec?command=sh'],
      ['POST', '/api/v1/namespaces/shop/serviceaccounts/default/token'],
      ['GET', '/api/v1/pods'],
      ['GET', '/api/v1/nodes'],
      ['GET', '/api/v1/namespaces'],
      ['GET', '/apis/rbac.authorization.k8s.io/v1/clusterrolebindings'],
      // The platform's own namespaces through a kind the console doesn't list there.
      ['GET', '/apis/fluxcd.controlplane.io/v1/namespaces/flux-system/resourcesets'],
      ['GET', '/apis/fluxcd.controlplane.io/v1/namespaces/shop/resourcesetinputproviders'],
      ['GET', '/api/v1/namespaces/shop/resourcequotas'],
      ['GET', '/api/v1/namespaces/shop/resourcequotas/other'],
      // Writes, and reads with a write's method.
      ['POST', '/api/v1/namespaces/shop/pods'],
      ['DELETE', '/api/v1/namespaces/shop'],
      ['PATCH', '/apis/serving.knative.dev/v1/namespaces/shop/services'],
      ['PUT', '/api/v1/namespaces/shop/pods'],
      ['GET', '/apis/authentication.k8s.io/v1/selfsubjectreviews'],
      ['POST', '/apis/authentication.k8s.io/v1/tokenreviews'],
      ['POST', '/apis/authorization.k8s.io/v1/subjectaccessreviews'],
      // A name that isn't one: traversal, encodings, another query.
      ['GET', '/api/v1/namespaces/../secrets'],
      ['GET', '/api/v1/namespaces/shop/../kube-system/pods'],
      ['GET', '/api/v1/namespaces/shop%2Fsecrets'],
      ['GET', '/api/v1/namespaces/Shop/pods'],
      ['GET', '/api/v1/namespaces/shop/pods?watch=true'],
      ['GET', '/api/v1/namespaces/shop/pods?labelSelector=x'],
      ['GET', '/api/v1/namespaces/shop/pods/'],
      ['GET', '/api/v1/namespaces/shop/pods#'],
      ['GET', '/api/v1/namespaces//pods'],
      ['GET', 'https://evil.example/api/v1/namespaces/shop/pods'],
      ['GET', '//evil.example/api/v1/namespaces/shop/pods'],
      ['GET', '/api/v1/namespaces/shop/pods\n/api/v1/secrets'],
      ['GET', '/api/v1/namespaces/shop/events'],
      ['GET', '/api/v1/namespaces/shop/events?fieldSelector=type%3DWarning&limit=500&watch=true'],
      ['GET', '/apis/apps/v1/namespaces/shop/deployments'],
      ['GET', ''],
      ['GET', '/'],
    ]
    for (const [method, path] of refused) {
      expect(isAllowed(method, path), `${method} ${path}`).toBe(false)
    }
  })
})

describe('reading as the user', () => {
  const recorder = (answer: (r: KubeRequest) => { status: number, body: any }) => {
    const seen: KubeRequest[] = []
    return { seen, transport: async (r: KubeRequest) => { seen.push(r); return answer(r) } }
  }

  it('sends the user\'s token and nothing else of its own', async () => {
    const { seen, transport } = recorder(() => ({ status: 200, body: { items: [{ a: 1 }] } }))
    const out = await new KubeReader(transport, 'the-users-id-token').list(paths.pods('shop'))
    expect(out).toEqual({ ok: true, items: [{ a: 1 }] })
    expect(seen).toEqual([{ method: 'GET', path: '/api/v1/namespaces/shop/pods', token: 'the-users-id-token', body: undefined }])
  })

  it('never asks for a path that isn\'t on the list', async () => {
    const { seen, transport } = recorder(() => ({ status: 200, body: { items: [] } }))
    const kube = new KubeReader(transport, 't')
    for (const path of ['/api/v1/namespaces/shop/secrets', paths.pods('../kube-system'), paths.namespace('shop/secrets'), `${paths.pods('shop')}?watch=1`]) {
      await expect(kube.list(path), path).rejects.toBeInstanceOf(KubeError)
      await expect(kube.get(path), path).rejects.toBeInstanceOf(KubeError)
    }
    expect(seen).toEqual([])
  })

  it('takes a refusal and a missing kind as answers', async () => {
    const kube = (status: number) => new KubeReader(recorder(() => ({ status, body: { kind: 'Status' } })).transport, 't')
    expect(await kube(403).list(paths.pods('shop'))).toEqual({ ok: false, why: 'denied' })
    expect(await kube(404).list(paths.scaledJobs('shop'))).toEqual({ ok: false, why: 'missing' })
    expect(await kube(403).get(paths.quota('shop'))).toEqual({ ok: false, why: 'denied' })
    expect(await kube(404).get(paths.provider('shop'))).toEqual({ ok: false, why: 'missing' })
  })

  it('ends the session when the cluster doesn\'t accept the token', async () => {
    const kube = new KubeReader(recorder(() => ({ status: 401, body: null })).transport, 't')
    await expect(kube.list(paths.providers())).rejects.toBeInstanceOf(KubeUnauthorized)
    await expect(kube.whoami()).rejects.toBeInstanceOf(KubeUnauthorized)
  })

  it('fails on anything unexpected, without passing the answer on', async () => {
    const kube = new KubeReader(recorder(() => ({ status: 500, body: { message: 'etcd leader changed: internal detail' } })).transport, 't')
    const err = await kube.list(paths.pods('shop')).catch(e => e)
    expect(err).toBeInstanceOf(KubeError)
    expect(err.message).not.toContain('etcd')
  })

  it('asks the cluster who the token is', async () => {
    const { seen, transport } = recorder(() => ({
      status: 201,
      body: { status: { userInfo: { username: 'authelia:alice', groups: ['authelia:maxcloud-developers', 'system:authenticated'] } } },
    }))
    expect(await new KubeReader(transport, 'tok').whoami()).toEqual({
      username: 'authelia:alice', groups: ['authelia:maxcloud-developers', 'system:authenticated'],
    })
    expect(seen[0]).toMatchObject({ method: 'POST', path: '/apis/authentication.k8s.io/v1/selfsubjectreviews', token: 'tok' })
    await expect(new KubeReader(recorder(() => ({ status: 201, body: {} })).transport, 't').whoami()).rejects.toBeInstanceOf(KubeError)
  })
})
