<script setup lang="ts">
// What the cluster itself remembers of past deploys: the revisions Knative
// keeps for each service. (mcloud history reads the GitOps history from
// GitHub; the console has no GitHub login.)
import type { RevisionsResponse } from '~~/shared/types'

const props = defineProps<{ app: string }>()
const { data, status, error, refresh } = useApi<RevisionsResponse>(() => `/api/apps/${props.app}/revisions`, { server: false, lazy: true })
defineExpose({ refresh })
</script>

<template>
  <section class="block">
    <div class="blockhead">
      <h2>Revisions</h2>
    </div>
    <div class="card">
      <PanelNote v-if="error" :message="errorText(error)" />
      <PanelNote v-else-if="!data" :pending="true" />
      <p v-else-if="!data.revisions.length" class="note">
        No revisions: the app has no HTTP functions deployed.
      </p>
      <table v-else class="rows" :class="{ busy: status === 'pending' }">
        <thead>
          <tr>
            <th>Revision</th>
            <th>Created</th>
            <th>Image</th>
            <th>Traffic</th>
            <th>Ready</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in data.revisions" :key="r.name">
            <td class="name">
              {{ r.name }}
            </td>
            <td data-label="Created" class="nowrap" :title="r.created">
              {{ r.age }}
            </td>
            <td data-label="Image" class="mono">
              {{ r.image }}
            </td>
            <td data-label="Traffic" class="num">
              <template v-if="r.traffic">
                {{ r.traffic }}%
              </template>
              <span v-else class="dim">-</span>
            </td>
            <td data-label="Ready">
              <ReadyCell :cond="r.ready" />
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
