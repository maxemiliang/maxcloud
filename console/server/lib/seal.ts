// Sealed cookie values: AES-256-GCM, so the browser can neither read nor
// change what it carries. The purpose ("session", "login") is authenticated
// with the value, so one kind of cookie can't be passed off as another.
//
//   base64url( 0x01 | iv (12) | tag (16) | ciphertext )
import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto'

const VERSION = 1
const IV = 12
const TAG = 16

/** Seals data (anything JSON) with the first key. */
export function seal(keys: Buffer[], purpose: string, data: unknown): string {
  const key = keys[0]
  if (!key || key.length !== 32) throw new Error('seal: a 32-byte key is needed')
  const iv = randomBytes(IV)
  const cipher = createCipheriv('aes-256-gcm', key, iv)
  cipher.setAAD(Buffer.from(purpose, 'utf8'))
  const body = Buffer.concat([cipher.update(JSON.stringify(data), 'utf8'), cipher.final()])
  return Buffer.concat([Buffer.from([VERSION]), iv, cipher.getAuthTag(), body]).toString('base64url')
}

/** Opens a sealed value with any of the keys; null if none fits or it was changed. */
export function unseal<T>(keys: Buffer[], purpose: string, sealed: string | undefined | null): T | null {
  if (!sealed || sealed.length > 8192 || !/^[A-Za-z0-9_-]+$/.test(sealed)) return null
  const raw = Buffer.from(sealed, 'base64url')
  if (raw.length < 1 + IV + TAG + 2 || raw[0] !== VERSION) return null
  const iv = raw.subarray(1, 1 + IV)
  const tag = raw.subarray(1 + IV, 1 + IV + TAG)
  const body = raw.subarray(1 + IV + TAG)
  for (const key of keys) {
    try {
      const decipher = createDecipheriv('aes-256-gcm', key, iv)
      decipher.setAAD(Buffer.from(purpose, 'utf8'))
      decipher.setAuthTag(tag)
      const plain = Buffer.concat([decipher.update(body), decipher.final()]).toString('utf8')
      return JSON.parse(plain) as T
    } catch {
      // The wrong key, or not ours: try the next.
    }
  }
  return null
}
