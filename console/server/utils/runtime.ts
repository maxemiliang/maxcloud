// What the server is made of, built once from the configuration: the login
// provider, the session refresher, and how the cluster and the platform's
// apps API are reached (for real, or the mock's stand-ins).
import { randomBytes } from 'node:crypto'
import { loadConfig, type Config } from '../lib/config'
import { httpsTransport, type KubeTransport } from '../lib/kube'
import { mockTransport } from '../lib/mock'
import { Oidc } from '../lib/oidc'
import { cookieNames, Refresher, type CookieNames } from '../lib/session'

export interface Runtime {
  config: Config
  oidc: Oidc
  refresher: Refresher
  transport: KubeTransport
  cookies: CookieNames
}

let rt: Runtime | undefined

/** Throws a ConfigError when the configuration is no good (server/plugins/0.config.ts stops the server). */
export function runtime(): Runtime {
  if (!rt) {
    const config = loadConfig(process.env, () => randomBytes(32))
    const oidc = new Oidc(config.oidc)
    rt = {
      config,
      oidc,
      refresher: new Refresher(refreshToken => oidc.refresh(refreshToken)),
      transport: config.mock ? mockTransport() : httpsTransport(config.kube.url, config.kube.caFile),
      cookies: cookieNames(config.secureCookies),
    }
  }
  return rt
}
