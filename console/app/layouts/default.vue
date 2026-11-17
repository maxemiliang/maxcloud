<script setup lang="ts">
import type { Me } from '~~/shared/types'

const { data: me } = await useApi<Me>('/api/me', { key: 'me' })
</script>

<template>
  <div>
    <header class="top">
      <NuxtLink to="/" class="brand">maxcloud <span>console</span></NuxtLink>
      <div v-if="me" class="who">
        <span class="user" title="Logged in as">{{ me.user }}</span>
        <!-- A form post, not a link: logging out is the one thing here that changes anything. -->
        <form method="post" action="/auth/logout">
          <button type="submit">Log out</button>
        </form>
      </div>
    </header>
    <div v-if="me?.mock" class="mockbar">
      Mock mode: made-up apps, no cluster.
    </div>
    <main>
      <slot />
    </main>
  </div>
</template>
