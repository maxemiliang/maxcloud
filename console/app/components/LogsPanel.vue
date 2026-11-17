<script setup lang="ts">
// The app's logs from the platform's Loki (mcloud logs --since): gone pods
// and services scaled to zero included.
import type { LogsResponse, Panel } from '~~/shared/types'

const props = defineProps<{ app: string }>()
const RANGES = ['15m', '1h', '6h', '24h', '7d']
const LIMITS = [100, 500, 1000]
const since = ref('1h')
const limit = ref(500)
const filter = ref('')
const { data, status, refresh } = useApi<Panel<LogsResponse>>(() => `/api/apps/${props.app}/logs`, {
  server: false, lazy: true, query: { since, limit },
})

const entries = computed(() => {
  const all = data.value?.ok ? data.value.data.entries : []
  const f = filter.value.trim().toLowerCase()
  return f ? all.filter(e => e.line.toLowerCase().includes(f) || e.pod.toLowerCase().includes(f)) : all
})
// One pod's name is noise on every line; several are worth telling apart.
const manyPods = computed(() => new Set(entries.value.map(e => e.pod)).size > 1)
</script>

<template>
  <section class="block">
    <div class="blockhead">
      <h2>Logs</h2>
      <div class="tools">
        <label><span class="sr">Range</span>
          <select v-model="since">
            <option v-for="r in RANGES" :key="r" :value="r">last {{ r }}</option>
          </select>
        </label>
        <label><span class="sr">Lines</span>
          <select v-model.number="limit">
            <option v-for="n in LIMITS" :key="n" :value="n">{{ n }} lines</option>
          </select>
        </label>
        <label><span class="sr">Filter the lines shown</span>
          <input v-model="filter" type="search" placeholder="filter">
        </label>
        <button type="button" :disabled="status === 'pending'" @click="refresh()">
          Reload
        </button>
      </div>
    </div>
    <div class="card">
      <PanelNote v-if="!data" :pending="status === 'pending' || status === 'idle'" message="The console couldn't be reached." />
      <PanelNote v-else-if="!data.ok" :message="data.message" />
      <template v-else>
        <p class="note">
          <template v-if="!data.data.entries.length">
            No lines in the last {{ since }}.
          </template>
          <template v-else>
            {{ entries.length }} {{ entries.length === 1 ? 'line' : 'lines' }}<template v-if="filter.trim()">
              of {{ data.data.entries.length }}
            </template>, oldest first, times in UTC.
            <template v-if="data.data.limited">
              There were more: the newest {{ limit }} are shown.
            </template>
          </template>
        </p>
        <pre v-if="entries.length" class="logs" :class="{ busy: status === 'pending' }" tabindex="0"><template v-for="(e, i) in entries" :key="i"><span class="t">{{ clock(e.time) }}</span> <span v-if="manyPods" class="p">{{ e.pod }} </span>{{ e.line }}
</template></pre>
      </template>
    </div>
  </section>
</template>
