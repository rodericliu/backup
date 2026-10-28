<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { goDuration, shortTime } from '@/format'
import type { Host, Project, Schedule } from '@/types'
import GanttChart from '@/components/GanttChart.vue'

const route = useRoute()
const router = useRouter()
const hosts = ref<Host[]>([])
const projects = ref<Project[]>([])
const schedule = ref<Schedule | null>(null)
const loading = ref(false)
const hostId = ref<number | undefined>(route.query.host_id ? Number(route.query.host_id) : undefined)

// Windows: [hours before now, total hours].
const windows = [
  { label: '过去 12 小时 + 未来 24 小时', before: 12, hours: 36 },
  { label: '未来 24 小时', before: 0, hours: 24 },
  { label: '未来 48 小时', before: 0, hours: 48 },
  { label: '未来 7 天', before: 0, hours: 168 },
  { label: '过去 48 小时', before: 48, hours: 48 },
]
const win = ref(0)

async function load() {
  if (!hostId.value) return
  loading.value = true
  try {
    const w = windows[win.value]
    const from = new Date(Math.floor(Date.now() / 3600_000) * 3600_000 - w.before * 3600_000)
    ;[schedule.value, projects.value] = await Promise.all([
      api.get<Schedule>('/schedule', { host_id: hostId.value, from: from.toISOString(), hours: w.hours }),
      api.get<Project[]>('/projects', { host_id: hostId.value }),
    ])
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  hosts.value = (await api.get<Host[]>('/hosts')).filter((h) => !h.revoked_at)
  hostId.value ??= hosts.value.find((h) => h.status === 'online')?.id ?? hosts.value[0]?.id
  load()
})
watch([hostId, win], () => {
  router.replace({ query: { ...route.query, host_id: hostId.value } })
  load()
})

const rows = computed(() => [...projects.value].sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name)).map((p) => p.name))
const overloaded = computed(() => schedule.value?.hours.filter((h) => h.overloaded) ?? [])
const queued = computed(() => {
  const runs = schedule.value?.runs.filter((r) => !r.superseded && r.start) ?? []
  if (!runs.length) return null
  const waits = runs.map((r) => (new Date(r.start!).getTime() - new Date(r.at).getTime()) / 60000)
  return { max: Math.max(...waits), superseded: schedule.value!.runs.filter((r) => r.superseded).length }
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>排班预测</h2>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </div>
    <div class="toolbar">
      <el-select v-model="hostId" placeholder="选择主机" style="width: 200px">
        <el-option v-for="h in hosts" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
      <el-radio-group v-model="win">
        <el-radio-button v-for="(w, i) in windows" :key="i" :value="i">{{ w.label }}</el-radio-button>
      </el-radio-group>
    </div>

    <el-empty v-if="!hosts.length" description="还没有主机" />
    <el-card v-else v-loading="loading" shadow="never">
      <div v-if="schedule" class="summary">
        <span>时区 {{ schedule.timezone }}</span>
        <span>计划 {{ schedule.runs.length }} 次备份</span>
        <span v-if="queued">最长排队 {{ Math.round(queued.max) }} 分钟</span>
        <span v-if="queued?.superseded" class="danger">{{ queued.superseded }} 次计划将被覆盖跳过</span>
        <span v-if="overloaded.length" class="danger">
          {{ overloaded.length }} 个超负荷小时：
          <template v-for="(h, i) in overloaded.slice(0, 8)" :key="h.hour">{{ i ? '、' : '' }}{{ shortTime(h.hour) }}（{{ goDuration(h.total) }}）</template>
          <template v-if="overloaded.length > 8"> 等</template>
        </span>
        <span v-else class="ok">没有超负荷的小时</span>
      </div>
      <p class="muted note">
        预测使用与 agent 相同的排班与串行执行规则，耗时取近 14 天成功任务的平均值；实际执行画在每个项目的预测下方。调整偏移量请到“项目”页面编辑（可使用推荐偏移量）。
      </p>
      <GanttChart v-if="schedule" :schedule="schedule" :projects="rows" />
    </el-card>
  </div>
</template>

<style scoped>
.summary {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-bottom: 8px;
}
.ok {
  color: var(--el-color-success);
}
.note {
  font-size: 12px;
  margin: 0 0 12px;
}
</style>
