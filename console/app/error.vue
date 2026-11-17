<script setup lang="ts">
import type { NuxtError } from '#app'

const props = defineProps<{ error: NuxtError }>()
const title = computed(() => (props.error.statusCode === 404 ? 'Nothing here' : 'Something went wrong'))
useHead({ title: `${title.value} · maxcloud console` })
</script>

<template>
  <main>
    <div class="card login">
      <h1>{{ title }}</h1>
      <p>{{ error.statusCode === 404 ? (error.statusMessage && error.statusMessage !== 'Not Found' ? error.statusMessage : 'No such page.') : (error.message || 'The console hit an error.') }}</p>
      <p><a href="/">Back to your apps</a></p>
    </div>
  </main>
</template>
