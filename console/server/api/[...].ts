// Anything else under /api isn't there. Said here, so it's a plain 404 and
// not a rendered page.
export default defineEventHandler(() => {
  throw createError({ statusCode: 404, statusMessage: 'Not Found', message: 'No such API route.' })
})
