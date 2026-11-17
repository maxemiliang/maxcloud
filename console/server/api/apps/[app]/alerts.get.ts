// The app's alerts, from the platform's apps API (for its owners and admins).
import type { AlertsResponse } from '~~/shared/types'

export default defineEventHandler(event => panel<AlertsResponse>(event, 'alerts'))
