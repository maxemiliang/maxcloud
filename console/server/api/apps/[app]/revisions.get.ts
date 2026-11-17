// The revisions Knative keeps for the app's services.
import { appRevisions } from '../../../lib/apps'

export default defineEventHandler(async (event) => {
  try {
    return await appRevisions(kubeFor(event), appParam(event), Date.now())
  } catch (e) {
    fail(event, e)
  }
})
