// The app's metrics, from the platform's apps API (for its owners and admins).
import type { MetricsResponse } from '~~/shared/types'

export default defineEventHandler(event => panel<MetricsResponse>(event, 'metrics'))
