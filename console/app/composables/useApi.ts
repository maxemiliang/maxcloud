// Reading the console's own API. A 401 means the login ran out while the
// page was open: go and log in, and come back here.
import type { UseFetchOptions } from '#app'

export function useApi<T>(url: string | (() => string), opts: UseFetchOptions<T> = {}) {
  return useFetch(url, {
    ...opts,
    onResponseError({ response }) {
      if (response.status === 401 && import.meta.client) {
        const here = window.location.pathname + window.location.search
        window.location.assign(`/login?next=${encodeURIComponent(here)}`)
      }
    },
  })
}

/** What a failed read says, for the page. */
export function errorText(e: { statusCode?: number, data?: any, message?: string } | null | undefined): string {
  if (!e) return ''
  return e.data?.message || (e.statusCode === 401 ? 'Your login ran out.' : 'The console couldn\'t be reached. Try again in a moment.')
}
