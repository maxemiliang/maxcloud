// A panel that asks the platform's apps API: logs, metrics or alerts for
// one app, with the user's token. Always an answer the panel can show.
import type { H3Event } from 'h3'
import type { Panel } from '~~/shared/types'
import { appsApi, panelQuery, type Feature } from '../lib/appsapi'
import { mockAppsApi } from '../lib/mock'

export async function panel<T>(event: H3Event, feature: Feature): Promise<Panel<T>> {
  const rt = runtime()
  const session = requireSession(event)
  const app = appParam(event)
  const query = panelQuery(feature, getQuery(event))
  try {
    if (rt.config.mock) return mockAppsApi(Date.now(), session.idToken, app, feature, query)
    return await appsApi<T>({ base: rt.config.appsApi }, session.idToken, app, feature, query)
  } catch (e) {
    fail(event, e)
  }
}
