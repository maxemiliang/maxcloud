// Who is logged in: the username the cluster gave at login.
import type { Me } from '~~/shared/types'

export default defineEventHandler((event): Me => {
  return { user: requireSession(event).user, mock: runtime().config.mock }
})
