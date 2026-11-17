// The same callback at /callback, for running the console on this machine
// against the real cluster: the login client's only local redirect URI is
// mcloud's, http://127.0.0.1:18000/callback (docs/console.md).
export default defineEventHandler(loginCallback)
