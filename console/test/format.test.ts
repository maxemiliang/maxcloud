import { describe, expect, it } from 'vitest'
import { humanBytes, listText, ownersText, quantity, readyText, repoHref, safeHref, shortRev, shortShas, sourceText } from '../app/utils/format'

describe('wording, as mcloud has it', () => {
  it('shortens revisions', () => {
    expect(shortRev('refs/heads/main@sha1:2495009b8f00aa11')).toBe('main@2495009')
    expect(shortRev('main@sha1:2495009b8f00aa11')).toBe('main@2495009')
    expect(shortRev('')).toBe('-')
    expect(shortRev(undefined)).toBe('-')
    expect(shortShas('Applied revision: main@sha1:4f1c2d7e9a0b3c5d6e7f8091a2b3c4d5e6f70812')).toBe('Applied revision: main@sha1:4f1c2d7')
  })

  it('sizes buckets', () => {
    expect(humanBytes(0)).toBe('0 B')
    expect(humanBytes(1023)).toBe('1023 B')
    expect(humanBytes(1024)).toBe('1.0 KiB')
    expect(humanBytes(52_428_800)).toBe('50.0 MiB')
    expect(humanBytes(1_288_490_188)).toBe('1.2 GiB')
  })

  it('names the source and the owners', () => {
    const app = { name: 'shop', mode: 'gitops', owners: ['alice', 'bob'], registeredIn: 'cluster', repo: 'example-org/shop', path: 'deploy/k8s', branch: 'main' }
    expect(sourceText(app)).toBe('example-org/shop:deploy/k8s')
    expect(sourceText({ ...app, branch: 'staging' })).toBe('example-org/shop@staging:deploy/k8s')
    expect(sourceText({ ...app, repo: undefined, path: 'platform/apps/run-demo' })).toBe('the maxcloud repo:platform/apps/run-demo')
    expect(sourceText({ ...app, mode: 'direct' })).toBe('direct (mcloud deploy)')
    expect(ownersText(app)).toBe('alice, bob')
    expect(ownersText({ ...app, owners: [] })).toBe('admins')
    expect(listText(['pods'])).toBe('pods')
    expect(listText(['pods', 'quota', 'domains'])).toBe('pods, quota and domains')
  })

  it('says why something isn\'t ready', () => {
    expect(readyText({ status: 'True' })).toBe('')
    expect(readyText({ status: 'True' }, false)).toBe('')
    expect(readyText({ status: '' })).toBe('waiting for the controller')
    expect(readyText({ status: 'False', reason: 'HostInUse', message: 'another route has it' })).toBe('HostInUse: another route has it')
  })

  it('reads quantities', () => {
    expect(quantity('60m')).toBeCloseTo(0.06)
    expect(quantity('1Gi')).toBe(1024 ** 3)
    expect(quantity('288Mi')).toBe(288 * 1024 ** 2)
    expect(quantity('3')).toBe(3)
    expect(quantity('0')).toBe(0)
    expect(quantity('lots')).toBeNull()
  })

  it('only links to http(s) URLs and GitHub repos', () => {
    expect(safeHref('https://web--shop.makiya.dev')).toBe('https://web--shop.makiya.dev')
    expect(safeHref('http://api.shop.svc.cluster.local')).toBe('http://api.shop.svc.cluster.local')
    for (const bad of ['javascript:alert(1)', 'data:text/html,x', ' https://x', 'https://x y', '', undefined, '//x']) {
      expect(safeHref(bad), String(bad)).toBeUndefined()
    }
    expect(repoHref('example-org/shop')).toBe('https://github.com/example-org/shop')
    for (const bad of ['example-org', 'a/b/c', 'javascript:x/y', 'a/b?x', '', undefined]) {
      expect(repoHref(bad), String(bad)).toBeUndefined()
    }
  })
})
