<script setup lang="ts">
definePageMeta({ layout: false })

const route = useRoute()
// Only ever sent back to the server, which checks it's a path on this site.
const next = computed(() => (typeof route.query.next === 'string' ? route.query.next : '/'))
const loggedOut = computed(() => route.query.out === '1')

// Fixed sentences per code: nothing from the address bar is shown as it came.
const ERRORS: Record<string, string> = {
  expired: 'That login took too long, or was started in another browser. Try again.',
  state: 'That login wasn\'t started here. Try again.',
  denied: 'Authelia didn\'t let you in.',
  provider: 'Authelia couldn\'t finish the login. Try again in a moment.',
  cluster: 'The cluster couldn\'t be asked who you are. Try again in a moment.',
  not_allowed: 'The cluster doesn\'t accept this login.',
}
const error = computed(() => (typeof route.query.error === 'string' ? (ERRORS[route.query.error] ?? ERRORS.provider) : ''))

// Mock mode is told to the page by the server, not guessed.
const mock = useState('login-mock', () => (import.meta.server ? useRequestEvent()?.context.mock === true : false))

useHead({ title: 'Log in · maxcloud console' })
</script>

<template>
  <main>
    <div class="card login">
      <h1>maxcloud <span class="dim">console</span></h1>
      <p v-if="loggedOut">
        You're logged out of the console. Authelia and <code>mcloud</code> keep their own logins.
      </p>
      <p v-else>
        Your apps on maxcloud, as <code>mcloud status</code> shows them. You see what your login may see.
      </p>
      <p v-if="error" class="note bad" role="alert">
        {{ error }}
      </p>
      <form method="post" action="/auth/login">
        <input type="hidden" name="next" :value="next">
        <fieldset v-if="mock">
          <legend>Mock mode: log in as</legend>
          <label class="check"><input type="radio" name="user" value="alice" checked> alice, a developer who owns shop and notes</label>
          <label class="check"><input type="radio" name="user" value="admin"> admin, who sees every app</label>
        </fieldset>
        <button type="submit" class="primary">
          {{ mock ? 'Log in' : 'Log in with Authelia' }}
        </button>
      </form>
    </div>
  </main>
</template>
