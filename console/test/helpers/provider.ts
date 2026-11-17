// A made-up OpenID Connect provider, shaped like Authelia's: discovery,
// keys, and ID tokens signed with a key made for the test.
import { createHash, createSign, generateKeyPairSync, type KeyObject } from 'node:crypto'

export interface FakeProvider {
  issuer: string
  kid: string
  privateKey: KeyObject
  jwks: () => { keys: any[] }
  discovery: () => Record<string, string>
  /** An ID token for claims, signed by this provider (or as the options say). */
  idToken: (claims: Record<string, unknown>, opts?: { alg?: string, kid?: string, key?: KeyObject, signature?: string }) => string
}

const b64 = (v: unknown) => Buffer.from(typeof v === 'string' ? v : JSON.stringify(v)).toString('base64url')

export function fakeProvider(issuer: string, kid = 'main'): FakeProvider {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  return {
    issuer,
    kid,
    privateKey,
    jwks: () => ({ keys: [{ ...publicKey.export({ format: 'jwk' }), kid, use: 'sig', alg: 'RS256' }] }),
    discovery: () => ({
      issuer,
      authorization_endpoint: `${issuer}/api/oidc/authorization`,
      token_endpoint: `${issuer}/api/oidc/token`,
      jwks_uri: `${issuer}/jwks.json`,
      revocation_endpoint: `${issuer}/api/oidc/revocation`,
    }),
    idToken(claims, opts = {}) {
      const head = `${b64({ alg: opts.alg ?? 'RS256', typ: 'JWT', kid: opts.kid ?? kid })}.${b64(claims)}`
      const sig = opts.signature ?? createSign('RSA-SHA256').update(head).sign(opts.key ?? privateKey).toString('base64url')
      return `${head}.${sig}`
    },
  }
}

export const s256 = (verifier: string) => createHash('sha256').update(verifier).digest('base64url')
