<script setup lang="ts">
// The platform's alerts about the app, now (mcloud alerts).
import type { AlertsResponse, Panel } from '~~/shared/types'

const props = defineProps<{ app: string }>()
const { data, status, refresh } = useApi<Panel<AlertsResponse>>(() => `/api/apps/${props.app}/alerts`, { server: false, lazy: true })
defineExpose({ refresh })

const since = (iso?: string) => {
  const t = iso ? Date.parse(iso) : NaN
  if (Number.isNaN(t)) return ''
  const m = Math.max(0, Math.round((Date.now() - t) / 60_000))
  return m < 60 ? `for ${m}m` : m < 48 * 60 ? `for ${Math.floor(m / 60)}h` : `for ${Math.floor(m / 1440)}d`
}
</script>

<template>
  <section class="block">
    <div class="blockhead">
      <h2>Alerts</h2>
    </div>
    <div class="card">
      <PanelNote v-if="!data" :pending="status === 'pending' || status === 'idle'" message="The console couldn't be reached." />
      <PanelNote v-else-if="!data.ok" :message="data.message" />
      <p v-else-if="!data.data.alerts.length" class="note">
        <StatusMark status="True" word="none" /> No alerts about this app.
      </p>
      <ul v-else class="plain">
        <li v-for="a in data.data.alerts" :key="a.name + JSON.stringify(a.labels)" class="alert">
          <div class="head">
            <b>{{ a.name }}</b>
            <span class="tag" :class="a.state">{{ a.state }}</span>
            <span v-if="a.severity" class="dim">{{ a.severity }}</span>
            <span class="dim">{{ since(a.activeAt) }}</span>
          </div>
          <div v-if="a.summary">
            {{ a.summary }}
          </div>
          <div v-if="a.description" class="why">
            {{ a.description }}
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>
