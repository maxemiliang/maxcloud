// One app (mcloud status --json).
import { appStatus } from '../../../lib/apps'

export default defineEventHandler(async (event) => {
  try {
    return await appStatus(kubeFor(event), appParam(event), Date.now())
  } catch (e) {
    fail(event, e)
  }
})
