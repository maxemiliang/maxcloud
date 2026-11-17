// The server doesn't start with a configuration it can't trust: no
// cookie-sealing key, no origin, a URL that isn't https, a CA file that
// isn't there. It says what's
// wrong and exits, so the pod shows the reason instead of serving logins
// that can't be kept safe.
import { ConfigError } from '../lib/config'

export default defineNitroPlugin(() => {
  try {
    const { config } = runtime()
    if (config.mock) {
      console.warn('maxcloud console: mock mode (CONSOLE_MOCK=1). Made-up apps and logins, no cluster.')
    }
  } catch (e) {
    const why = e instanceof ConfigError ? e.message : String((e as Error)?.message ?? e)
    console.error(`maxcloud console: not starting, the configuration is no good:\n${why}\n(docs/console.md lists the variables)`)
    process.exit(1)
  }
})
