<script setup lang="ts">
import type { AppsResponse, AppSummary } from '~~/shared/types'

const { data, error, refresh, status } = await useApi<AppsResponse>('/api/apps')

// An app's environments and previews sit under it.
const rows = computed(() => {
  const apps = data.value?.apps ?? []
  const names = new Set(apps.map(a => a.name))
  const children = new Map<string, AppSummary[]>()
  for (const a of apps) {
    if (a.environmentOf && names.has(a.environmentOf)) {
      if (!children.has(a.environmentOf)) children.set(a.environmentOf, [])
      children.get(a.environmentOf)!.push(a)
    }
  }
  const out: { app: AppSummary, env: boolean }[] = []
  for (const a of apps) {
    if (a.environmentOf && names.has(a.environmentOf)) continue
    out.push({ app: a, env: false })
    for (const c of children.get(a.name) ?? []) out.push({ app: c, env: true })
  }
  return out
})

const shortName = (a: AppSummary) => (a.environmentOf && a.name.startsWith(`${a.environmentOf}--`) ? a.name.slice(a.environmentOf.length + 2) : a.name)

useHead({ title: 'Apps · maxcloud console' })
</script>

<template>
  <div>
    <div class="pagehead">
      <h1>Apps</h1>
      <span v-if="data" class="dim">{{ data.apps.length }} registered</span>
      <div class="tools">
        <button type="button" :disabled="status === 'pending'" @click="refresh()">
          Refresh
        </button>
      </div>
    </div>

    <p v-if="error" class="note bad" role="alert">
      {{ errorText(error) }}
    </p>
    <p v-else-if="data && !data.apps.length" class="note card">
      No apps yet. <code>mcloud init</code> and <code>mcloud deploy</code> register the first one.
    </p>
    <div v-else-if="data" class="card">
      <table class="rows">
        <thead>
          <tr>
            <th>App</th>
            <th>Health</th>
            <th>Services</th>
            <th>Deploy</th>
            <th>Source</th>
            <th>Owners</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="{ app, env } in rows" :key="app.name" :class="{ env }">
            <td class="name">
              <NuxtLink :to="`/apps/${app.name}`">{{ env ? shortName(app) : app.name }}</NuxtLink>
            </td>
            <td data-label="Health" class="nowrap">
              <StatusMark :status="healthStatus(app.health)" :exists="app.health !== 'unknown'" :word="HEALTH_WORD[app.health]" />
              {{ HEALTH_WORD[app.health] }}
            </td>
            <td data-label="Services" class="num">
              <template v-if="app.services">
                {{ app.services.ready }}/{{ app.services.total }} ready
              </template>
              <span v-else class="dim">-</span>
            </td>
            <td data-label="Deploy">
              <template v-if="app.mode === 'direct'">
                <span class="dim">direct</span>
              </template>
              <template v-else-if="app.deploy">
                <StatusMark :status="app.deploy.status" />
                <span class="mono"> {{ shortRev(app.deploy.revision) }}</span>
              </template>
              <span v-else class="dim">not created yet</span>
              <div v-if="app.sync?.error" class="why">
                maxcloud.yaml not applied
              </div>
            </td>
            <td data-label="Source" class="mono">
              {{ sourceText(app) }}
            </td>
            <td data-label="Owners">
              <span :class="{ dim: !app.owners.length }">{{ ownersText(app) }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
