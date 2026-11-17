// How things are worded on the pages: mcloud's own wording
// (internal/cli/status.go, status_extras.go, apps.go), so a line here reads
// like the same line in a terminal.
import type { App, Cond, Health } from '~~/shared/types'

/** "refs/heads/main@sha1:2495009b8…" or "main@sha1:2495009b8…" becomes "main@2495009". */
export function shortRev(rev: string | undefined): string {
  if (!rev) return '-'
  const i = rev.indexOf('@sha1:')
  if (i < 0) return truncate(rev, 20)
  const ref = rev.slice(0, i).replace(/^refs\/heads\//, '')
  return `${ref}@${rev.slice(i + 6, i + 13)}`
}

/** Cuts the commit hashes in a line of Flux's down to seven characters. */
export function shortShas(s: string): string {
  return s.replace(/(@sha1:|sha256:)([0-9a-f]{7})[0-9a-f]{5,57}\b/g, '$1$2')
}

/** "a, b and c". */
export function listText(words: string[]): string {
  return words.length < 2 ? words.join('') : `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
}

export function firstLine(s: string | undefined): string {
  return (s ?? '').trim().split('\n')[0] ?? ''
}

export function truncate(s: string | undefined, n: number): string {
  const line = firstLine(s)
  return line.length <= n ? line : `${line.slice(0, n - 3)}...`
}

/** 1288490188 becomes "1.2 GiB". */
export function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`
  let div = 1024
  let exp = 0
  for (let m = n / 1024; m >= 1024; m /= 1024) {
    div *= 1024
    exp++
  }
  return `${(n / div).toFixed(1)} ${'KMGTPE'[exp]}iB`
}

/** Where an app's manifests come from. */
export function sourceText(a: App): string {
  if (!a.mode) return 'not in the registry'
  if (a.mode === 'direct') return 'direct (mcloud deploy)'
  const repo = a.repo || 'the maxcloud repo'
  const path = a.path || 'deploy/k8s'
  return a.branch && a.branch !== 'main' ? `${repo}@${a.branch}:${path}` : `${repo}:${path}`
}

export function ownersText(a: App): string {
  return a.owners.length ? a.owners.join(', ') : 'admins'
}

/** The words for a mark, for people who don't see it. */
export function condWord(status: string): string {
  return status === 'True' ? 'ready' : status === 'False' ? 'failed' : 'in progress'
}

/** A Ready condition as mcloud's tables put it: nothing when it's ready, else the reason and message. */
export function readyText(c: Cond, observed = true): string {
  if (c.status === 'True' && observed) return ''
  if (c.status === '') return 'waiting for the controller'
  let out = c.reason ?? ''
  if (c.message) out += `${out ? ': ' : ''}${truncate(c.message, 120)}`
  return out
}

export const HEALTH_WORD: Record<Health, string> = {
  ok: 'healthy',
  progressing: 'rolling out',
  failing: 'failing',
  unknown: 'unknown',
}

/** The status a health maps to, for its mark. */
export function healthStatus(h: Health): string {
  return h === 'ok' ? 'True' : h === 'failing' ? 'False' : h === 'progressing' ? 'Unknown' : ''
}

const SUFFIX: Record<string, number> = {
  n: 1e-9, u: 1e-6, m: 1e-3, k: 1e3, M: 1e6, G: 1e9, T: 1e12,
  Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4,
}

/** A Kubernetes quantity ("60m", "288Mi", "3") as a number, or null. */
export function quantity(s: string): number | null {
  const m = /^([0-9.]+)([a-zA-Z]*)$/.exec(s.trim())
  if (!m) return null
  const n = Number(m[1])
  if (Number.isNaN(n)) return null
  if (!m[2]) return n
  const f = SUFFIX[m[2]]
  return f === undefined ? null : n * f
}

/** A log line's time of day, in UTC. */
export function clock(iso: string): string {
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : t.toISOString().slice(11, 19)
}

/** 0.0123 becomes "1.2%". */
export function percent(ratio: number | null | undefined): string {
  if (ratio === null || ratio === undefined) return '-'
  return `${(ratio * 100).toFixed(ratio > 0 && ratio < 0.1 ? 1 : 0)}%`
}

/** 0.112 becomes "112 ms", 1.84 "1.84 s". */
export function seconds(s: number | null | undefined): string {
  if (s === null || s === undefined) return '-'
  return s < 1 ? `${Math.round(s * 1000)} ms` : `${s.toFixed(2)} s`
}

export function compact(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e4) return `${(n / 1e3).toFixed(1)}k`
  return String(Math.round(n))
}

/** A URL the cluster reported, if it's safe to link to: http(s) only. */
export function safeHref(url: string | undefined): string | undefined {
  return url && /^https?:\/\/[^\s]+$/i.test(url) ? url : undefined
}

/** The app's repo on GitHub, if its entry names one. */
export function repoHref(repo: string | undefined): string | undefined {
  return repo && /^[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/.test(repo) ? `https://github.com/${repo}` : undefined
}
