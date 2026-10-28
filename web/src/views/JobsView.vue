<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { useEvents } from '@/composables/events'
import { bytes, datetime, jobDuration, shortTime, statusText, triggerText, typeText } from '@/format'
import type { ChainView, Host, Job, List, Project, RestorePoint } from '@/types'
import JobStatus from '@/components/JobStatus.vue'
import JobDrawer from '@/components/JobDrawer.vue'
import RestoreGuide from '@/components/RestoreGuide.vue'

const route = useRoute()
const router = useRouter()
const hosts = ref<Host[]>([])
const projects = ref<Project[]>([])
const tab = ref(route.query.tab === 'chains' ? 'chains' : 'history')
watch(tab, (t) => router.replace({ query: { ...route.query, tab: t === 'chains' ? 'chains' : undefined } }))

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
const shortcuts = [
  { text: '今天', value: () => [new Date(new Date().setHours(0, 0, 0, 0)), new Date()] },
  { text: '最近 24 小时', value: () => [new Date(Date.now() - 86400_000), new Date()] },
  { text: '最近 7 天', value: () => [new Date(Date.now() - 7 * 86400_000), new Date()] },
  { text: '最近 30 天', value: () => [new Date(Date.now() - 30 * 86400_000), new Date()] },
]

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
const chainStatus: Record<string, { text: string; type: 'success' | 'primary' | 'danger' | 'info' }> = {
  active: { text: '可用', type: 'success' },
  running: { text: '全量进行中', type: 'primary' },
  failed: { text: '全量失败', type: 'danger' },
  deleted: { text: '已清理', type: 'info' },
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
  <div>
    <div class="page-header">
      <h2>任务</h2>
      <el-button :icon="Refresh" @click="tab === 'chains' ? loadChains() : loadJobs()">刷新</el-button>
    </div>

    <div class="toolbar">
      <el-select v-model="filter.host_id" clearable placeholder="全部主机" style="width: 160px">
        <el-option v-for="h in hosts" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
      <el-select v-model="filter.project_id" clearable filterable placeholder="全部项目" style="width: 180px">
        <el-option v-for="p in projectOptions" :key="p.id" :label="p.deleted_at ? `${p.name}（已删除）` : p.name" :value="p.id" />
      </el-select>
      <template v-if="tab === 'history'">
        <el-select v-model="filter.type" clearable placeholder="全部类型" style="width: 110px">
          <el-option label="全量" value="full" />
          <el-option label="增量" value="incr" />
        </el-select>
        <el-select v-model="filter.status" multiple collapse-tags clearable placeholder="全部状态" style="width: 200px">
          <el-option v-for="s in statusOptions" :key="s" :label="statusText[s]" :value="s" />
        </el-select>
        <el-date-picker
          v-model="filter.range"
          type="datetimerange"
          :shortcuts="shortcuts"
          range-separator="至"
          start-placeholder="计划时间从"
          end-placeholder="到"
          style="width: 360px"
        />
      </template>
    </div>

    <el-tabs v-model="tab">
      <el-tab-pane label="任务历史" name="history">
        <el-table v-loading="loading" :data="jobs" class="clickable" @row-click="(j: Job) => openJob(j.id)">
          <el-table-column label="计划时间" width="170"><template #default="{ row }">{{ datetime(row.scheduled_at) }}</template></el-table-column>
          <el-table-column label="项目" min-width="120">
            <template #default="{ row }"><b>{{ row.project }}</b></template>
          </el-table-column>
          <el-table-column label="主机" min-width="110"><template #default="{ row }">{{ hostName(row.host_id) }}</template></el-table-column>
          <el-table-column label="类型" width="70"><template #default="{ row }">{{ typeText[row.type] }}</template></el-table-column>
          <el-table-column label="触发" width="70"><template #default="{ row }">{{ triggerText[row.trigger] }}</template></el-table-column>
          <el-table-column label="状态" min-width="150">
            <template #default="{ row }"><JobStatus :status="row.status" :code="row.error_code" :message="row.error_message" /></template>
          </el-table-column>
          <el-table-column label="开始" width="110"><template #default="{ row }">{{ row.started_at ? shortTime(row.started_at) : '-' }}</template></el-table-column>
          <el-table-column label="耗时" width="90"><template #default="{ row }">{{ jobDuration(row.started_at, row.finished_at) }}</template></el-table-column>
          <el-table-column label="大小" width="100" align="right"><template #default="{ row }">{{ row.compressed_size ? bytes(row.compressed_size) : '-' }}</template></el-table-column>
          <el-table-column label="bitmap" width="80">
            <template #default="{ row }">
              <template v-if="row.type === 'incr' && (row.status === 'success' || row.status === 'warning')">
                <el-tag :type="row.used_bitmap ? 'success' : 'warning'" size="small">{{ row.used_bitmap ? '是' : '否' }}</el-tag>
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
      </el-tab-pane>

      <el-tab-pane label="备份链" name="chains">
        <el-empty v-if="!filter.project_id" description="请选择一个项目" />
        <div v-else v-loading="chainsLoading">
          <div class="toolbar">
            <el-checkbox v-model="showDeleted">显示已清理的链</el-checkbox>
            <span class="muted">共 {{ chains.length }} 条链，{{ points.length }} 个可恢复时间点。绿色为可恢复的备份点。</span>
          </div>
          <el-empty v-if="!chains.length" description="没有备份链" />
          <el-card v-for="c in chains" :key="c.id" shadow="never" class="chain" :class="{ deleted: c.status === 'deleted' }">
            <div class="chain-head">
              <span>
                <b>{{ datetime(c.started_at) }}</b> 开始
                <el-tag :type="chainStatus[c.status]?.type" size="small" class="ml">{{ chainStatus[c.status]?.text ?? c.status }}</el-tag>
              </span>
              <span class="muted">{{ c.jobs.length }} 份 · {{ bytes(chainSize(c)) }} · 最近 {{ datetime(c.last_job_at) }}</span>
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
                >恢复指引</el-button>
              </div>
            </div>
          </el-card>
        </div>
      </el-tab-pane>
    </el-tabs>

    <JobDrawer :job-id="detailId" @close="closeJob" @restore="(id) => (restoreId = id)" @open="openJob" />
    <RestoreGuide :job-id="restoreId" @close="restoreId = null" />
  </div>
</template>

<style scoped>
.clickable :deep(.el-table__row) {
  cursor: pointer;
}
.pager {
  margin-top: 12px;
  justify-content: flex-end;
}
.chain {
  margin-bottom: 12px;
}
.chain.deleted {
  opacity: 0.6;
}
.chain-head {
  display: flex;
  justify-content: space-between;
  margin-bottom: 10px;
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
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  padding: 6px 10px;
  min-width: 96px;
  cursor: pointer;
  font-size: 12px;
  background: #fff;
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
