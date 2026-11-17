// The console changes nothing. Only two routes take anything but a read,
// logging in and logging out, and both only from the console's own pages.
const LOGIN_ROUTES = new Set(['/auth/login', '/auth/logout'])

export default defineEventHandler((event) => {
  const { config } = runtime()
  const method = event.method
  if (method === 'GET' || method === 'HEAD') return
  const path = event.path.split('?')[0]!
  if (method !== 'POST' || !LOGIN_ROUTES.has(path)) {
    throw createError({ statusCode: 405, statusMessage: 'Method Not Allowed' })
  }
  // Cross-site request forgery: a form on another site can post here, and
  // the browser says where it came from. Only the console's own origin may.
  // (SameSite=Lax already keeps the session cookie out of such posts.)
  const origin = getRequestHeader(event, 'origin')
  const allowed = new Set([config.origin])
  if (config.mock) {
    // The mock runs wherever the dev server listens.
    const host = getRequestHeader(event, 'host')
    if (host) allowed.add(`http://${host}`).add(`https://${host}`)
  }
  const sameOrigin = origin ? allowed.has(origin) : getRequestHeader(event, 'sec-fetch-site') === 'same-origin'
  if (!sameOrigin) {
    throw createError({ statusCode: 403, statusMessage: 'Forbidden', message: 'This request didn\'t come from the console.' })
  }
  const length = Number(getRequestHeader(event, 'content-length') ?? 0)
  if (!(length <= 4096)) {
    throw createError({ statusCode: 413, statusMessage: 'Payload Too Large' })
  }
})
