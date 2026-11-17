import { randomBytes } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { seal, unseal } from '../server/lib/seal'

const key = () => randomBytes(32)

describe('sealed cookies', () => {
  it('opens what it sealed', () => {
    const k = [key()]
    const v = seal(k, 'session', { user: 'alice', n: 1 })
    expect(v).toMatch(/^[A-Za-z0-9_-]+$/)
    expect(unseal(k, 'session', v)).toEqual({ user: 'alice', n: 1 })
  })

  it('hides what it holds', () => {
    const v = seal([key()], 'session', { idToken: 'eyJhbGciOiJSUzI1NiJ9.secret-token-body.sig' })
    expect(Buffer.from(v, 'base64url').toString('latin1')).not.toContain('secret-token-body')
  })

  it('seals differently each time', () => {
    const k = [key()]
    expect(seal(k, 'session', { a: 1 })).not.toBe(seal(k, 'session', { a: 1 }))
  })

  it('refuses a value that was changed', () => {
    const k = [key()]
    const raw = Buffer.from(seal(k, 'session', { user: 'alice' }), 'base64url')
    for (const i of [0, 5, 20, raw.length - 1]) {
      const copy = Buffer.from(raw)
      copy[i] = copy[i]! ^ 1
      expect(unseal(k, 'session', copy.toString('base64url'))).toBeNull()
    }
  })

  it('refuses another key', () => {
    expect(unseal([key()], 'session', seal([key()], 'session', { a: 1 }))).toBeNull()
  })

  it('keeps a login cookie from passing as a session', () => {
    const k = [key()]
    const login = seal(k, 'login', { state: 's' })
    expect(unseal(k, 'session', login)).toBeNull()
    expect(unseal(k, 'login', login)).toEqual({ state: 's' })
  })

  it('opens with an older key after the key was rotated', () => {
    const old = key()
    const v = seal([old], 'session', { a: 1 })
    const next = key()
    expect(unseal([next, old], 'session', v)).toEqual({ a: 1 })
    // New values are sealed with the first key.
    expect(unseal([next], 'session', seal([next, old], 'session', { b: 2 }))).toEqual({ b: 2 })
  })

  it('takes rubbish for nothing', () => {
    const k = [key()]
    for (const v of [undefined, null, '', 'x', '!!!', 'AQ', 'a'.repeat(9000), '../../etc/passwd']) {
      expect(unseal(k, 'session', v)).toBeNull()
    }
  })

  it('needs a real key', () => {
    expect(() => seal([], 'session', {})).toThrow()
    expect(() => seal([Buffer.alloc(16)], 'session', {})).toThrow()
  })
})
