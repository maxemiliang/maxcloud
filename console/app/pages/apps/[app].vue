<script setup lang="ts">
// One app, laid out as `mcloud status` prints it: the app's lines, then a
// table per kind, then its problems. Below that, what mcloud has other
// commands for: alerts, metrics, logs, and the revisions the cluster keeps.
import type { Status } from '~~/shared/types'

const route = useRoute()
const name = computed(() => String(route.params.app))
const { data: st, error, refresh, status } = await useApi<Status>(() => `/api/apps/${name.value}`)

if (error.value?.statusCode === 404) {
  throw createError({ statusCode: 404, statusMessage: `No app ${name.value} on the cluster`, fatal: true })
}

const alerts = useTemplateRef('alerts')
const metrics = useTemplateRef('metrics')
const revisions = useTemplateRef('revisions')
function refreshAll() {
  refresh()
  alerts.value?.refresh()
  metrics.value?.refresh()
  revisions.value?.refresh()
}

// Watching a deploy: read again every 15 seconds while the tab is in front.
const live = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
watch(live, (on) => {
  clearInterval(timer)
  if (on) {
    timer = setInterval(() => {
      if (document.visibilityState === 'visible') refreshAll()
    }, 15_000)
  }
})
onBeforeUnmount(() => clearInterval(timer))

const RESTRICTED_WORD: Record<string, string> = {
  pods: 'pods',
  workloads: 'workloads',
  quota: 'quota',
  domains: 'domains',
  problems: 'problems',
}
const restricted = computed(() => (st.value?.restricted ?? []).map(r => RESTRICTED_WORD[r] ?? r))
const quotaLabel = (resource: string) => resource.replace(/^requests\./, '')

useHead({ title: () => `${name.value} · maxcloud console` })
</script>

<template>
  <div>
    <p class="crumb">
      <NuxtLink to="/">Apps</NuxtLink> <span class="dim">/</span>
    </p>
    <div class="pagehead">
      <h1>{{ name }}</h1>
      <span v-if="st" class="nowrap">
        <StatusMark :status="healthStatus(st.health)" :exists="st.health !== 'unknown'" :word="HEALTH_WORD[st.health]" />
        {{ HEALTH_WORD[st.health] }}
      </span>
      <div class="tools">
        <label class="check"><input v-model="live" type="checkbox"> follow</label>
        <button type="button" :disabled="status === 'pending'" @click="refreshAll()">
          Refresh
        </button>
      </div>
    </div>

    <p v-if="error" class="note bad" role="alert">
      {{ errorText(error) }}
    </p>

    <template v-if="st">
      <p v-if="restricted.length" class="note warn">
        You're not one of this app's owners, so its {{ listText(restricted) }} aren't shown:
        those are for its owners and admins. What's below is what every developer may see.
      </p>

      <div class="card" :class="{ busy: status === 'pending' }">
        <dl class="kv">
          <dt>Source</dt>
          <dd>
            <span v-if="!st.registered" class="dim">not in the registry</span>
            <template v-else>
              <a v-if="repoHref(st.repo)" :href="repoHref(st.repo)" rel="noopener noreferrer" class="mono">{{ sourceText(st) }}</a>
              <span v-else class="mono">{{ sourceText(st) }}</span>
              <span v-if="st.registeredIn === 'git'" class="tag">registered in git</span>
              <span v-if="st.environmentOf" class="tag">environment of {{ st.environmentOf }}</span>
            </template>
          </dd>
          <template v-if="st.registered">
            <dt>Owners</dt>
            <dd :class="{ dim: !st.owners.length }">
              {{ ownersText(st) }}
            </dd>
          </template>
          <template v-if="st.fetch && st.fetch.status !== 'True'">
            <dt>Fetch</dt>
            <dd><StatusMark :status="st.fetch.status" /> {{ shortShas(firstLine(st.fetch.message)) }}</dd>
          </template>
          <dt>Deploy</dt>
          <dd>
            <template v-if="st.mode === 'direct'">
              direct: applied by <code>mcloud deploy</code>, nothing from git
            </template>
            <template v-else-if="st.deploy">
              <StatusMark :status="st.deploy.status" /> {{ shortShas(firstLine(st.deploy.message)) || shortRev(st.deploy.revision) }}
            </template>
            <template v-else>
              <span class="dim">not created yet</span> (Flux picks up a new entry within a minute)
            </template>
          </dd>
          <template v-if="st.sync?.error">
            <dt>maxcloud.yaml</dt>
            <dd>
              <StatusMark status="False" /> not applied: {{ st.sync.error }}
              <div class="why">
                the pushed file at {{ shortRev(st.sync.revision) }}; the app keeps what it had until a push fixes it
              </div>
            </dd>
          </template>
          <template v-if="st.quota.length">
            <dt>Quota</dt>
            <dd class="quota">
              <span v-for="q in st.quota" :key="q.resource">
                {{ quotaLabel(q.resource) }} <span class="mono">{{ q.used }}/{{ q.hard }}</span>
                <meter v-if="quantity(q.used) !== null && quantity(q.hard)" min="0" :max="quantity(q.hard)!" :value="quantity(q.used)!" :high="quantity(q.hard)! * 0.85" :title="`${q.used} of ${q.hard}`" />
              </span>
            </dd>
          </template>
        </dl>
      </div>

      <section v-if="st.services.length" class="block">
        <div class="blockhead">
          <h2>Services</h2>
        </div>
        <div class="card">
          <table class="rows">
            <thead>
              <tr>
                <th>Service</th>
                <th>URL</th>
                <th>Ready</th>
                <th>Revision</th>
                <th>Pods</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in st.services" :key="s.name">
                <td class="name">
                  {{ s.name }}
                </td>
                <td data-label="URL" class="url">
                  <a v-if="s.public && safeHref(s.url)" :href="safeHref(s.url)" rel="noopener noreferrer">{{ s.url }}</a>
                  <template v-else>
                    <span class="mono">{{ s.url || '-' }}</span> <span class="dim">(private)</span>
                  </template>
                </td>
                <td data-label="Ready">
                  <ReadyCell :cond="s.ready" />
                </td>
                <td data-label="Revision" class="mono dim">
                  {{ s.revision || '-' }}
                </td>
                <td data-label="Pods" class="num">
                  {{ s.pods ?? '-' }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="st.workloads.length" class="block">
        <div class="blockhead">
          <h2>Workloads</h2>
        </div>
        <div class="card">
          <table class="rows">
            <thead>
              <tr>
                <th>Workload</th>
                <th>Kind</th>
                <th>State</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="w in st.workloads" :key="w.kind + w.name">
                <td class="name">
                  {{ w.name }}
                </td>
                <td data-label="Kind">
                  {{ w.kind }}
                </td>
                <td data-label="State">
                  <StatusMark :status="w.ready ? 'True' : 'False'" /> {{ w.state }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="st.databases.length" class="block">
        <div class="blockhead">
          <h2>Database</h2>
        </div>
        <div class="card">
          <table class="rows">
            <thead>
              <tr>
                <th>Database</th>
                <th>Instances</th>
                <th>State</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in st.databases" :key="d.name">
                <td class="name">
                  {{ d.name }}
                </td>
                <td data-label="Instances" class="num">
                  {{ d.instances }}
                </td>
                <td data-label="State">
                  <StatusMark :status="d.healthy ? 'True' : 'False'" /> {{ d.phase }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="st.buckets.length" class="block">
        <div class="blockhead">
          <h2>Buckets</h2>
        </div>
        <div class="card">
          <table class="rows">
            <thead>
              <tr>
                <th>Bucket</th>
                <th>In Object Storage</th>
                <th>Access</th>
                <th>Size</th>
                <th>Ready</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="b in st.buckets" :key="b.name">
                <td class="name">
                  {{ b.name }}
                </td>
                <td data-label="In Object Storage" class="mono dim">
                  {{ b.bucket || '-' }}
                </td>
                <td data-label="Access">
                  <template v-if="b.public">
                    public <a v-if="safeHref(b.url)" :href="safeHref(b.url)" rel="noopener noreferrer">{{ b.url }}</a>
                  </template>
                  <template v-else>
                    private
                  </template>
                </td>
                <td data-label="Size" class="num">
                  <template v-if="b.sizeBytes !== null">
                    {{ humanBytes(b.sizeBytes) }}<span v-if="b.objects !== null" class="dim"> ({{ b.objects }} objects)</span>
                  </template>
                  <span v-else class="dim">-</span>
                </td>
                <td data-label="Ready">
                  <ReadyCell :cond="b.ready" :observed="b.observed" />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="st.domains.length" class="block">
        <div class="blockhead">
          <h2>Domains</h2>
        </div>
        <div class="card">
          <table class="rows">
            <thead>
              <tr>
                <th>Domain</th>
                <th>Service</th>
                <th>Ready</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in st.domains" :key="d.host">
                <td class="name">
                  <a :href="safeHref(d.url) ?? `https://${d.host}/`" rel="noopener noreferrer">{{ d.url || `https://${d.host}/` }}</a>
                  <span v-if="d.proxied" class="dim"> (Cloudflare)</span>
                </td>
                <td data-label="Service" class="mono">
                  {{ d.service }}
                </td>
                <td data-label="Ready">
                  <ReadyCell :cond="d.ready" :observed="d.observed" />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="block">
        <div class="blockhead">
          <h2>Problems</h2>
        </div>
        <div class="card">
          <ul v-if="st.problems.length" class="plain problems">
            <li v-for="p in st.problems" :key="p">
              {{ p }}
            </li>
          </ul>
          <p v-else-if="st.restricted.includes('problems')" class="note">
            Only the app's owners and admins see its pods' problems and recent warnings.
          </p>
          <p v-else class="note">
            <StatusMark status="True" word="none" /> No pods in trouble, no warnings in the last 15 minutes.
          </p>
        </div>
      </section>

      <AlertsPanel ref="alerts" :app="name" />
      <MetricsPanel ref="metrics" :app="name" />
      <LogsPanel :app="name" />
      <RevisionsPanel ref="revisions" :app="name" />
    </template>
  </div>
</template>
