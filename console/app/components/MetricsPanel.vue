<script setup lang="ts">
// Requests, 5xx, p95 latency and pods per service; queue backlog; jobs (mcloud metrics).
import type { MetricsResponse, Panel } from '~~/shared/types'

const props = defineProps<{ app: string }>()
const WINDOWS = ['1h', '6h', '24h', '7d']
const window = ref('1h')
const { data, status, refresh } = useApi<Panel<MetricsResponse>>(() => `/api/apps/${props.app}/metrics`, {
  server: false, lazy: true, query: { window },
})
defineExpose({ refresh })
</script>

<template>
  <section class="block">
    <div class="blockhead">
      <h2>Metrics</h2>
      <div class="tools">
        <label><span class="sr">Window</span>
          <select v-model="window">
            <option v-for="w in WINDOWS" :key="w" :value="w">last {{ w }}</option>
          </select>
        </label>
      </div>
    </div>
    <div class="card">
      <PanelNote v-if="!data" :pending="status === 'pending' || status === 'idle'" message="The console couldn't be reached." />
      <PanelNote v-else-if="!data.ok" :message="data.message" />
      <template v-else>
        <p v-if="!data.data.services.length" class="note">
          No requests to report.
        </p>
        <table v-else class="rows" :class="{ busy: status === 'pending' }">
          <thead>
            <tr>
              <th>Service</th>
              <th>Requests</th>
              <th>Per second</th>
              <th>5xx</th>
              <th>p95</th>
              <th>Pods</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in data.data.services" :key="s.name">
              <td class="name">
                {{ s.name }}
              </td>
              <td data-label="Requests" class="num">
                {{ compact(s.requests) }}
              </td>
              <td data-label="Per second" class="num">
                {{ s.requestsPerSecond.toFixed(2) }}
              </td>
              <td data-label="5xx" class="num">
                {{ compact(s.errors) }} <span class="dim">({{ percent(s.errorRatio) }})</span>
              </td>
              <td data-label="p95" class="num">
                {{ seconds(s.p95Seconds) }}
              </td>
              <td data-label="Pods" class="num">
                {{ s.pods ?? '-' }}<template v-if="s.podsWanted !== undefined && s.podsWanted !== null && s.podsWanted !== s.pods">
                  <span class="dim"> (wants {{ s.podsWanted }})</span>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="data.data.queues.length || data.data.jobs.running || data.data.jobs.failed" class="note">
          <template v-for="q in data.data.queues" :key="q.name">
            queue <code>{{ q.name }}</code>: backlog {{ compact(q.backlog) }}.
          </template>
          Jobs: {{ data.data.jobs.running }} running, {{ data.data.jobs.failed }} failed.
        </p>
      </template>
    </div>
  </section>
</template>
