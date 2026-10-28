<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, errorMessage } from '@/api'
import { useEvents } from '@/composables/events'
import { ago, bytes, datetime, errorCodeText, shortTime, typeText } from '@/format'
import type { Dashboard, Progress } from '@/types'
import HostCard from '@/components/HostCard.vue'
import JobStatus from '@/components/JobStatus.vue'

const data = ref<Dashboard | null>(null)
const loading = ref(false)
// Progress of running jobs from server-sent events (PRD D-5).
const progress = reactive(new Map<string, Progress>())

async function load() {
  loading.value = true
  try {
    data.value = await api.get<Dashboard>('/dashboard')
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

const stats = computed(() => {
  const t = data.value?.today
  return [
    { label: '成功', value: t?.success ?? 0, cls: 'ok' },
    { label: '警告', value: t?.warning ?? 0, cls: 'warn' },
    { label: '失败', value: t?.failed ?? 0, cls: 'bad' },
    { label: '跳过', value: t?.skipped ?? 0, cls: '' },
    { label: '备份量', value: bytes(t?.bytes ?? 0), cls: '' },
  ]
})

const staleRow = ({ row }: { row: { stale: boolean } }) => (row.stale ? 'stale-row' : '')

const staleCount = computed(() => data.value?.projects.filter((p) => p.stale).length ?? 0)
</script>

<template>
  <div v-loading="loading && !data">
    <div class="stats">
      <el-card v-for="s in stats" :key="s.label" shadow="never" class="stat">
        <div class="stat-label">今日{{ s.label }}</div>
        <div class="stat-value" :class="s.cls">{{ s.value }}</div>
      </el-card>
    </div>

    <h3>主机</h3>
    <el-empty v-if="data && !data.hosts.length" description="还没有主机，请在“主机”页面生成注册 token 接入 agent" />
    <div class="card-grid">
      <HostCard v-for="h in data?.hosts" :key="h.id" :host="h" :progress="h.state.current ? progress.get(h.state.current.id) : undefined" />
    </div>

    <div class="two-col">
      <el-card shadow="never">
        <template #header>
          项目健康
          <el-tag v-if="staleCount" type="danger" size="small">{{ staleCount }} 个项目超过两个全量周期无成功备份</el-tag>
        </template>
        <el-table :data="data?.projects ?? []" size="small" :row-class-name="staleRow">
          <el-table-column label="项目" min-width="120">
            <template #default="{ row }">
              <router-link :to="{ name: 'jobs', query: { project_id: row.id } }">{{ row.name }}</router-link>
              <el-tag v-if="!row.enabled" size="small" type="info">已停用</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="host_name" label="主机" min-width="110" />
          <el-table-column label="最近成功全量" min-width="130">
            <template #default="{ row }">
              <el-tooltip :content="datetime(row.last_full)" :disabled="!row.last_full"><span>{{ row.last_full ? ago(row.last_full) : '无' }}</span></el-tooltip>
            </template>
          </el-table-column>
          <el-table-column label="最近成功增量" min-width="130">
            <template #default="{ row }">
              <el-tooltip :content="datetime(row.last_incr)" :disabled="!row.last_incr"><span>{{ row.last_incr ? ago(row.last_incr) : '无' }}</span></el-tooltip>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag :type="row.stale ? 'danger' : 'success'" size="small">{{ row.stale ? '过期' : '正常' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-card>

      <el-card shadow="never">
        <template #header>最近失败（7 天）</template>
        <el-empty v-if="data && !data.recent_failures.length" description="没有失败的任务" :image-size="60" />
        <el-table v-else :data="data?.recent_failures ?? []" size="small">
          <el-table-column label="时间" width="110">
            <template #default="{ row }">{{ shortTime(row.scheduled_at) }}</template>
          </el-table-column>
          <el-table-column label="项目" min-width="110">
            <template #default="{ row }">{{ row.project }} {{ typeText[row.type] }}</template>
          </el-table-column>
          <el-table-column label="原因" min-width="160">
            <template #default="{ row }">
              <router-link :to="{ name: 'jobs', query: { id: row.id } }">{{ errorCodeText[row.error_code] ?? row.error_code ?? '-' }}</router-link>
              <div class="muted ellipsis">{{ row.error_message }}</div>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }"><JobStatus :status="row.status" /></template>
          </el-table-column>
        </el-table>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.stats {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 16px;
}
.stat-label {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.stat-value {
  font-size: 26px;
  font-weight: 600;
  margin-top: 6px;
}
.stat-value.ok {
  color: var(--el-color-success);
}
.stat-value.warn {
  color: var(--el-color-warning);
}
.stat-value.bad {
  color: var(--el-color-danger);
}
h3 {
  margin: 20px 0 12px;
}
.two-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  margin-top: 20px;
}
@media (max-width: 1200px) {
  .two-col {
    grid-template-columns: 1fr;
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
