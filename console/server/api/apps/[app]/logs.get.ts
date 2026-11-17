// The app's logs, from the platform's apps API (for its owners and admins).
import type { LogsResponse } from '~~/shared/types'

export default defineEventHandler(event => panel<LogsResponse>(event, 'logs'))
