<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { goDuration, shortTime } from '@/format'
import type { Host, Project, Schedule } from '@/types'
import GanttChart from '@/components/GanttChart.vue'
import PageHeader from '@/components/PageHeader.vue'
import { t } from '@/i18n'

const route = useRoute()
const router = useRouter()
const hosts = ref<Host[]>([])
const projects = ref<Project[]>([])
const schedule = ref<Schedule | null>(null)
const loading = ref(false)
const hostId = ref<number | undefined>(route.query.host_id ? Number(route.query.host_id) : undefined)

// Windows: [hours before now, total hours].
const windows = [
  { label: () => t('schedule.windowAround'), before: 12, hours: 36 },
  { label: () => t('schedule.windowNext', { n: 24 }), before: 0, hours: 24 },
  { label: () => t('schedule.windowNext', { n: 48 }), before: 0, hours: 48 },
  { label: () => t('schedule.windowNextDays', { n: 7 }), before: 0, hours: 168 },
  { label: () => t('schedule.windowPast', { n: 48 }), before: 48, hours: 48 },
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
  <div class="bk-page">
    <PageHeader :title="$t('schedule.title')" :eyebrow="$t('nav.backup')" :description="$t('schedule.description')">
      <template #actions>
        <el-button :icon="Refresh" @click="load">{{ $t('common.refresh') }}</el-button>
      </template>
    </PageHeader>
    <div class="toolbar">
      <el-select v-model="hostId" :placeholder="$t('schedule.selectHost')" style="width: 200px">
        <el-option v-for="h in hosts" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
      <el-radio-group v-model="win">
        <el-radio-button v-for="(w, i) in windows" :key="i" :value="i">{{ w.label() }}</el-radio-button>
      </el-radio-group>
    </div>

    <div v-if="!hosts.length" class="bk-panel"><el-empty :description="$t('schedule.noHosts')" :image-size="72" /></div>
    <div v-else v-loading="loading" class="bk-panel">
      <div v-if="schedule" class="summary">
        <div class="metric"><span class="section-label">{{ $t('schedule.timezone') }}</span><b>{{ schedule.timezone }}</b></div>
        <div class="metric"><span class="section-label">{{ $t('schedule.plannedRuns') }}</span><b class="bk-num">{{ $t('schedule.times', { n: schedule.runs.length }) }}</b></div>
        <div v-if="queued" class="metric"><span class="section-label">{{ $t('schedule.maxQueue') }}</span><b class="bk-num">{{ $t('schedule.minutes', { n: Math.round(queued.max) }) }}</b></div>
        <div v-if="queued?.superseded" class="metric"><span class="section-label">{{ $t('schedule.superseded') }}</span><b class="bk-num danger">{{ $t('schedule.times', { n: queued.superseded }) }}</b></div>
        <div class="metric wide">
          <span class="section-label">{{ $t('schedule.load') }}</span>
          <span v-if="overloaded.length" class="danger">
            <b>{{ $t('schedule.overloadedHours', { n: overloaded.length }) }}</b>
            <template v-for="(h, i) in overloaded.slice(0, 8)" :key="h.hour">{{ i ? $t('common.listSep') : '' }}{{ shortTime(h.hour) }}{{ $t('common.paren', { s: goDuration(h.total) }) }}</template>
            <template v-if="overloaded.length > 8">{{ $t('common.etc') }}</template>
          </span>
          <b v-else class="ok">{{ $t('schedule.noOverload') }}</b>
        </div>
      </div>
      <div class="bk-panel__body">
        <GanttChart v-if="schedule" :schedule="schedule" :projects="rows" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.summary {
  display: flex;
  flex-wrap: wrap;
  border-bottom: 1px solid var(--bk-border);
}
.metric {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 14px var(--bk-panel-padding);
  border-right: 1px solid var(--bk-border);
}
.metric .section-label {
  margin: 0;
}
.metric.wide {
  flex: 1;
  min-width: 240px;
  border-right: none;
}
.ok {
  color: var(--el-color-success);
}

</style>
