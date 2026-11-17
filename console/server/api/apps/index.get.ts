// The apps this login can see (mcloud apps list), with each one's health.
import { listApps } from '../../lib/apps'

export default defineEventHandler(async (event) => {
  try {
    return await listApps(kubeFor(event))
  } catch (e) {
    fail(event, e)
  }
})
