<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { CircleCheck, CircleClose, Coin, Monitor, Refresh, Remove, Warning, WarningFilled } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { useEvents } from '@/composables/events'
import { ago, bytes, datetime, errorCodeText, shortTime, typeText } from '@/format'
import type { Dashboard, Progress } from '@/types'
import HostCard from '@/components/HostCard.vue'
import JobStatus from '@/components/JobStatus.vue'
import PageHeader from '@/components/PageHeader.vue'
import { t } from '@/i18n'

const data = ref<Dashboard | null>(null)
const loading = ref(false)
// Progress of running jobs from server-sent events (PRD D-5).
const progress = reactive(new Map<string, Progress>())

async function load() {
  loading.value = true
  try {
    data.value = await api.get<Dashboard>('/dashboard')
    lastUpdated.value = new Date()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}

let reloadTimer: ReturnType<typeof setTimeout> | undefined
function reloadSoon() {
  clearTimeout(reloadTimer)
  reloadTimer = setTimeout(load, 1000)
}

useEvents('host', (h) => {
  if (!data.value) return
  const i = data.value.hosts.findIndex((x) => x.id === h.id)
  if (i >= 0) data.value.hosts[i] = h
  else reloadSoon()
})

useEvents('job', (u) => {
  if (u.progress) progress.set(u.job.id, u.progress)
  const h = data.value?.hosts.find((x) => x.id === u.job.host_id)
  if (!h) return
  if (u.job.status === 'running') {
    h.state.current = u.job
    h.state.queue = h.state.queue.filter((j) => j.id !== u.job.id)
  } else if (h.state.current?.id === u.job.id) {
    h.state.current = undefined
    progress.delete(u.job.id)
  }
  // Statistics, health and failures change when a job finishes.
  if (u.job.status !== 'running' && u.job.status !== 'pending') reloadSoon()
})

let timer: ReturnType<typeof setInterval>
onMounted(() => {
  load()
  timer = setInterval(load, 60_000)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  clearTimeout(reloadTimer)
})

const lastUpdated = ref<Date | null>(null)

const stats = computed(() => {
  const d = data.value?.today
  return [
    { label: t('dashboard.todaySuccess'), value: d?.success ?? 0, tone: 'success', icon: CircleCheck },
    { label: t('dashboard.todayWarning'), value: d?.warning ?? 0, tone: 'warning', icon: Warning },
    { label: t('dashboard.todayFailed'), value: d?.failed ?? 0, tone: 'danger', icon: CircleClose },
    { label: t('dashboard.todaySkipped'), value: d?.skipped ?? 0, tone: 'info', icon: Remove },
    { label: t('dashboard.todayBytes'), value: bytes(d?.bytes ?? 0), tone: 'primary', icon: Coin },
  ]
})

const staleRow = ({ row }: { row: { stale: boolean } }) => (row.stale ? 'stale-row' : '')

const staleCount = computed(() => data.value?.projects.filter((p) => p.stale).length ?? 0)
</script>

<template>
  <div v-loading="loading && !data" class="bk-page">
    <PageHeader :title="$t('nav.dashboard')" :eyebrow="$t('nav.monitor')" :description="$t('dashboard.description')">
      <template #actions>
        <span class="muted updated">{{ $t('dashboard.updatedAt', { time: lastUpdated ? lastUpdated.toLocaleTimeString() : '-' }) }}</span>
        <el-button :icon="Refresh" :loading="loading" @click="load">{{ $t('common.refresh') }}</el-button>
      </template>
    </PageHeader>

    <div class="stats">
      <div v-for="s in stats" :key="s.label" class="bk-panel stat" :class="`tone-${s.tone}`">
        <div class="stat-icon"><el-icon :size="20"><component :is="s.icon" /></el-icon></div>
        <div>
          <div class="stat-label">{{ s.label }}</div>
          <div class="stat-value bk-num">{{ s.value }}</div>
        </div>
      </div>
    </div>

    <section>
      <div class="section-head">
        <h2><el-icon><Monitor /></el-icon>{{ $t('nav.hosts') }}</h2>
        <span v-if="data?.hosts.length" class="muted">{{ $t('dashboard.hostCount', { n: data.hosts.length }) }}</span>
      </div>
      <div v-if="data && !data.hosts.length" class="bk-panel">
        <el-empty :description="$t('dashboard.noHosts')" :image-size="72" />
      </div>
      <div class="card-grid">
        <HostCard v-for="h in data?.hosts" :key="h.id" :host="h" :progress="h.state.current ? progress.get(h.state.current.id) : undefined" />
      </div>
    </section>

    <div class="two-col">
      <div class="bk-panel">
        <div class="bk-panel__head">
          <div class="bk-panel__title">{{ $t('dashboard.health') }}</div>
          <el-tag v-if="staleCount" type="danger" size="small"><el-icon class="tag-icon"><WarningFilled /></el-icon>{{ $t('dashboard.staleCount', { n: staleCount }) }}</el-tag>
        </div>
        <el-table :data="data?.projects ?? []" size="small" :row-class-name="staleRow">
          <el-table-column :label="$t('job.project')" min-width="120">
            <template #default="{ row }">
              <router-link :to="{ name: 'jobs', query: { project_id: row.id } }" class="strong">{{ row.name }}</router-link>
              <el-tag v-if="!row.enabled" size="small" type="info" class="ml">{{ $t('dashboard.disabled') }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="host_name" :label="$t('job.host')" min-width="110" />
          <el-table-column :label="$t('dashboard.lastFull')" min-width="130">
            <template #default="{ row }">
              <el-tooltip :content="datetime(row.last_full)" :disabled="!row.last_full"><span>{{ row.last_full ? ago(row.last_full) : $t('common.none') }}</span></el-tooltip>
            </template>
          </el-table-column>
          <el-table-column :label="$t('dashboard.lastIncr')" min-width="130">
            <template #default="{ row }">
              <el-tooltip :content="datetime(row.last_incr)" :disabled="!row.last_incr"><span>{{ row.last_incr ? ago(row.last_incr) : $t('common.none') }}</span></el-tooltip>
            </template>
          </el-table-column>
          <el-table-column :label="$t('job.status')" width="90">
            <template #default="{ row }">
              <el-tag :type="row.stale ? 'danger' : 'success'" size="small">{{ row.stale ? $t('dashboard.stale') : $t('dashboard.healthy') }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div class="bk-panel">
        <div class="bk-panel__head">
          <div class="bk-panel__title">{{ $t('dashboard.recentFailures') }}</div>
          <span class="bk-panel__subtitle">{{ $t('dashboard.last7Days') }}</span>
        </div>
        <el-empty v-if="data && !data.recent_failures.length" :description="$t('dashboard.noFailures')" :image-size="60" />
        <el-table v-else :data="data?.recent_failures ?? []" size="small">
          <el-table-column :label="$t('dashboard.time')" width="110">
            <template #default="{ row }"><span class="bk-num">{{ shortTime(row.scheduled_at) }}</span></template>
          </el-table-column>
          <el-table-column :label="$t('job.project')" min-width="110">
            <template #default="{ row }">{{ row.project }} {{ typeText[row.type] }}</template>
          </el-table-column>
          <el-table-column :label="$t('dashboard.reason')" min-width="160">
            <template #default="{ row }">
              <router-link :to="{ name: 'jobs', query: { id: row.id } }">{{ errorCodeText[row.error_code] ?? row.error_code ?? '-' }}</router-link>
              <div class="muted ellipsis">{{ row.error_message }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="$t('job.status')" width="90">
            <template #default="{ row }"><JobStatus :status="row.status" /></template>
          </el-table-column>
        </el-table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.updated {
  font-size: 0.8125rem;
  margin-inline-end: 4px;
}
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(190px, 100%), 1fr));
  gap: 16px;
}
.stat {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 18px var(--bk-panel-padding);
}
.stat-icon {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  inline-size: 44px;
  block-size: 44px;
  border-radius: 10px;
  background: var(--tone-bg);
  color: var(--tone);
}
.tone-success {
  --tone: var(--el-color-success);
  --tone-bg: var(--el-color-success-light-9);
}
.tone-warning {
  --tone: var(--el-color-warning);
  --tone-bg: var(--el-color-warning-light-9);
}
.tone-danger {
  --tone: var(--el-color-danger);
  --tone-bg: var(--el-color-danger-light-9);
}
.tone-info {
  --tone: var(--el-color-info);
  --tone-bg: var(--el-color-info-light-9);
}
.tone-primary {
  --tone: var(--el-color-primary);
  --tone-bg: var(--el-color-primary-light-9);
}
.stat-label {
  color: var(--bk-secondary);
  font-size: 0.8125rem;
}
.stat-value {
  margin-top: 2px;
  color: var(--bk-on-surface);
  font-size: 1.625rem;
  font-weight: 650;
  letter-spacing: -0.02em;
  line-height: 1.2;
}
.tone-success .stat-value,
.tone-warning .stat-value,
.tone-danger .stat-value {
  color: var(--tone);
}
.section-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin-bottom: 12px;
}
.section-head h2 {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 1rem;
  font-weight: 600;
}
.section-head h2 .el-icon {
  color: var(--bk-secondary);
}
.two-col {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: var(--bk-gap);
}
.strong {
  font-weight: 600;
}
.ml {
  margin-left: 6px;
}
.tag-icon {
  margin-right: 4px;
  vertical-align: -2px;
}
@media (max-width: 1200px) {
  .two-col {
    grid-template-columns: minmax(0, 1fr);
  }
}
.ellipsis {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
:deep(.stale-row) {
  --el-table-tr-bg-color: var(--el-color-danger-light-9);
}
</style>
