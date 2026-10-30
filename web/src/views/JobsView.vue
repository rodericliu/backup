<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { useEvents } from '@/composables/events'
import { bytes, chainStatusText, datetime, jobDuration, shortTime, statusText, triggerText, typeText } from '@/format'
import type { ChainView, Host, Job, List, Project, RestorePoint } from '@/types'
import PageHeader from '@/components/PageHeader.vue'
import JobStatus from '@/components/JobStatus.vue'
import JobDrawer from '@/components/JobDrawer.vue'
import RestoreGuide from '@/components/RestoreGuide.vue'
import { t } from '@/i18n'

const route = useRoute()
const router = useRouter()
const hosts = ref<Host[]>([])
const projects = ref<Project[]>([])
const tab = ref(route.query.tab === 'chains' ? 'chains' : 'history')
watch(tab, (v) => router.replace({ query: { ...route.query, tab: v === 'chains' ? 'chains' : undefined } }))

const q = (k: string) => (typeof route.query[k] === 'string' ? (route.query[k] as string) : undefined)

// History (PRD J-2).
const filter = reactive({
  host_id: q('host_id') ? Number(q('host_id')) : undefined,
  project_id: q('project_id') ? Number(q('project_id')) : undefined,
  type: q('type') ?? '',
  status: q('status') ? q('status')!.split(',') : ([] as string[]),
  range: null as [Date, Date] | null,
})
const page = ref(1)
const pageSize = ref(50)
const jobs = ref<Job[]>([])
const total = ref(0)
const loading = ref(false)

async function loadJobs() {
  loading.value = true
  try {
    const r = await api.get<List<Job>>('/jobs', {
      host_id: filter.host_id,
      project_id: filter.project_id,
      type: filter.type,
      status: filter.status.join(','),
      from: filter.range?.[0]?.toISOString(),
      to: filter.range?.[1]?.toISOString(),
      page: page.value,
      page_size: pageSize.value,
    })
    jobs.value = r.items
    total.value = r.total
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
watch(
  () => [filter.host_id, filter.project_id, filter.type, filter.status, filter.range],
  () => {
    page.value = 1
    loadJobs()
    if (filter.project_id && tab.value === 'chains') loadChains()
  },
  { deep: true },
)
watch([page, pageSize], loadJobs)

const statusOptions = ['pending', 'running', 'success', 'warning', 'failed', 'skipped', 'canceled']
const projectOptions = computed(() => projects.value.filter((p) => !filter.host_id || p.host_id === filter.host_id))
const hostName = (id: number) => hosts.value.find((h) => h.id === id)?.name ?? id
const shortcuts = computed(() => [
  { text: t('jobs.today'), value: () => [new Date(new Date().setHours(0, 0, 0, 0)), new Date()] },
  { text: t('jobs.lastHours', { n: 24 }), value: () => [new Date(Date.now() - 86400_000), new Date()] },
  { text: t('jobs.lastDays', { n: 7 }), value: () => [new Date(Date.now() - 7 * 86400_000), new Date()] },
  { text: t('jobs.lastDays', { n: 30 }), value: () => [new Date(Date.now() - 30 * 86400_000), new Date()] },
])

// Live status of listed jobs (PRD D-5).
useEvents('job', (u) => {
  const i = jobs.value.findIndex((j) => j.id === u.job.id)
  if (i >= 0) jobs.value[i] = u.job
})

// Detail drawer, opened with ?id=.
const detailId = computed(() => q('id') ?? null)
function openJob(id: string) {
  router.push({ query: { ...route.query, id } })
}
function closeJob() {
  router.replace({ query: { ...route.query, id: undefined } })
}
const restoreId = ref<string | null>(null)

// Chains (PRD J-5).
const chains = ref<ChainView[]>([])
const points = ref<RestorePoint[]>([])
const showDeleted = ref(false)
const chainsLoading = ref(false)
async function loadChains() {
  if (!filter.project_id) return
  chainsLoading.value = true
  try {
    const r = await api.get<{ chains: ChainView[]; restore_points: RestorePoint[] }>(`/projects/${filter.project_id}/chains`, {
      include_deleted: showDeleted.value || undefined,
    })
    chains.value = r.chains
    points.value = r.restore_points
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    chainsLoading.value = false
  }
}
watch([tab, showDeleted], () => tab.value === 'chains' && loadChains())
const restorable = computed(() => new Set(points.value.map((p) => p.job_id)))
const chainTag: Record<string, 'success' | 'primary' | 'danger' | 'info'> = {
  active: 'success',
  running: 'primary',
  failed: 'danger',
  deleted: 'info',
}
const chainSize = (c: ChainView) => c.jobs.reduce((n, j) => n + (j.compressed_size ?? 0), 0)

onMounted(async () => {
  try {
    ;[hosts.value, projects.value] = await Promise.all([api.get<Host[]>('/hosts'), api.get<Project[]>('/projects', { include_deleted: true })])
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
  loadJobs()
  if (tab.value === 'chains') loadChains()
})
</script>

<template>
  <div class="bk-page">
    <PageHeader :title="$t('nav.jobs')" :eyebrow="$t('nav.backup')" :description="$t('jobs.description')">
      <template #actions>
        <el-button :icon="Refresh" @click="tab === 'chains' ? loadChains() : loadJobs()">{{ $t('common.refresh') }}</el-button>
      </template>
    </PageHeader>

    <el-tabs v-model="tab" class="page-tabs">
      <el-tab-pane :label="$t('nav.jobs')" name="history" />
      <el-tab-pane :label="$t('job.chain')" name="chains" />
    </el-tabs>

    <div class="bk-panel">
    <div class="toolbar">
      <el-select v-model="filter.host_id" clearable :placeholder="$t('projects.allHosts')" style="width: 160px">
        <el-option v-for="h in hosts" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
      <el-select v-model="filter.project_id" clearable filterable :placeholder="$t('jobs.allProjects')" style="width: 180px">
        <el-option v-for="p in projectOptions" :key="p.id" :label="p.deleted_at ? p.name + $t('common.paren', { s: $t('projects.deleted') }) : p.name" :value="p.id" />
      </el-select>
      <template v-if="tab === 'history'">
        <el-select v-model="filter.type" clearable :placeholder="$t('jobs.allTypes')" style="width: 130px">
          <el-option :label="typeText.full" value="full" />
          <el-option :label="typeText.incr" value="incr" />
        </el-select>
        <el-select v-model="filter.status" multiple collapse-tags clearable :placeholder="$t('jobs.allStatuses')" style="width: 200px">
          <el-option v-for="s in statusOptions" :key="s" :label="statusText[s]" :value="s" />
        </el-select>
        <el-date-picker
          v-model="filter.range"
          type="datetimerange"
          :shortcuts="shortcuts"
          :range-separator="$t('jobs.rangeTo')"
          :start-placeholder="$t('jobs.rangeFrom')"
          :end-placeholder="$t('jobs.rangeEnd')"
          style="width: 360px"
        />
      </template>
    </div>

      <template v-if="tab === 'history'">
        <el-table v-loading="loading" :data="jobs" class="clickable" @row-click="(j: Job) => openJob(j.id)">
          <el-table-column :label="$t('job.scheduledAt')" width="185"><template #default="{ row }"><span class="bk-num">{{ datetime(row.scheduled_at) }}</span></template></el-table-column>
          <el-table-column :label="$t('job.project')" min-width="120">
            <template #default="{ row }"><b>{{ row.project }}</b></template>
          </el-table-column>
          <el-table-column :label="$t('job.host')" min-width="110"><template #default="{ row }">{{ hostName(row.host_id) }}</template></el-table-column>
          <el-table-column :label="$t('job.type')" width="110"><template #default="{ row }">{{ typeText[row.type] }}</template></el-table-column>
          <el-table-column :label="$t('job.trigger')" width="100"><template #default="{ row }">{{ triggerText[row.trigger] }}</template></el-table-column>
          <el-table-column :label="$t('job.status')" min-width="150">
            <template #default="{ row }"><JobStatus :status="row.status" :code="row.error_code" :message="row.error_message" /></template>
          </el-table-column>
          <el-table-column :label="$t('job.startedAt')" width="110"><template #default="{ row }"><span class="bk-num">{{ row.started_at ? shortTime(row.started_at) : '-' }}</span></template></el-table-column>
          <el-table-column :label="$t('job.duration')" width="90"><template #default="{ row }"><span class="bk-num">{{ jobDuration(row.started_at, row.finished_at) }}</span></template></el-table-column>
          <el-table-column :label="$t('job.size')" width="100" align="right"><template #default="{ row }"><span class="bk-num">{{ row.compressed_size ? bytes(row.compressed_size) : '-' }}</span></template></el-table-column>
          <el-table-column label="bitmap" width="80">
            <template #default="{ row }">
              <template v-if="row.type === 'incr' && (row.status === 'success' || row.status === 'warning')">
                <el-tag :type="row.used_bitmap ? 'success' : 'warning'" size="small">{{ row.used_bitmap ? $t('jobs.yes') : $t('jobs.no') }}</el-tag>
              </template>
            </template>
          </el-table-column>
        </el-table>
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50, 100, 200]"
          layout="total, sizes, prev, pager, next, jumper"
          class="pager"
        />
      </template>

      <template v-else>
        <el-empty v-if="!filter.project_id" :description="$t('jobs.selectProject')" :image-size="72" />
        <div v-else v-loading="chainsLoading" class="bk-panel__body">
          <div class="toolbar chain-toolbar">
            <el-checkbox v-model="showDeleted">{{ $t('jobs.showDeletedChains') }}</el-checkbox>
            <span class="muted">{{ $t('jobs.chainSummary', { chains: chains.length, points: points.length }) }}</span>
          </div>
          <el-empty v-if="!chains.length" :description="$t('jobs.noChains')" />
          <el-card v-for="c in chains" :key="c.id" shadow="never" class="chain" :class="{ deleted: c.status === 'deleted' }">
            <div class="chain-head">
              <span>
                <i18n-t keypath="jobs.chainStarted"><template #time><b>{{ datetime(c.started_at) }}</b></template></i18n-t>
                <el-tag :type="chainTag[c.status]" size="small" class="ml">{{ chainStatusText[c.status] ?? c.status }}</el-tag>
              </span>
              <span class="muted">{{ $t('jobs.chainInfo', { n: c.jobs.length, size: bytes(chainSize(c)), time: datetime(c.last_job_at) }) }}</span>
            </div>
            <div class="chain-jobs">
              <div
                v-for="j in c.jobs"
                :key="j.id"
                class="chain-job"
                :class="[j.status, { restorable: restorable.has(j.id) && c.status !== 'deleted' }]"
                @click="openJob(j.id)"
              >
                <div class="cj-type">{{ typeText[j.type] }}</div>
                <div class="cj-time">{{ shortTime(j.finished_at ?? j.scheduled_at) }}</div>
                <div class="cj-size">{{ j.compressed_size ? bytes(j.compressed_size) : statusText[j.status] }}</div>
                <el-button
                  v-if="restorable.has(j.id) && c.status !== 'deleted'"
                  size="small"
                  link
                  type="primary"
                  @click.stop="restoreId = j.id"
                >{{ $t('jobDrawer.restoreGuide') }}</el-button>
              </div>
            </div>
          </el-card>
        </div>
      </template>
    </div>

    <JobDrawer :job-id="detailId" @close="closeJob" @restore="(id) => (restoreId = id)" @open="openJob" />
    <RestoreGuide :job-id="restoreId" @close="restoreId = null" />
  </div>
</template>

<style scoped>
.clickable :deep(.el-table__row) {
  cursor: pointer;
}
.page-tabs :deep(.el-tabs__header) {
  margin-block-end: 0;
}
.pager {
  justify-content: flex-end;
  padding: 12px var(--bk-panel-padding);
  border-top: 1px solid var(--bk-border);
}
.chain-toolbar {
  margin-bottom: 16px;
}
.chain {
  margin-bottom: 12px;
  background: var(--bk-subtle);
}
.chain.deleted {
  opacity: 0.6;
}
.chain-head {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: space-between;
  margin-bottom: 12px;
}
.ml {
  margin-left: 6px;
}
.chain-jobs {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.chain-job {
  border: 1px solid var(--bk-border);
  border-radius: 8px;
  padding: 8px 12px;
  min-width: 96px;
  cursor: pointer;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  background: var(--bk-surface);
  transition: border-color var(--bk-motion), box-shadow var(--bk-motion);
}
.chain-job:hover {
  box-shadow: 0 0 0 3px rgba(var(--bk-primary-rgb), 0.12);
}
.chain-job.restorable {
  border-color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}
.chain-job.failed {
  border-color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
}
.chain-job.skipped,
.chain-job.canceled {
  color: var(--el-text-color-secondary);
}
.chain-job.running,
.chain-job.pending {
  border-color: var(--el-color-primary);
}
.cj-type {
  font-weight: 600;
}
</style>
