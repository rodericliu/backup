<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, errorMessage } from '@/api'
import { useEvents } from '@/composables/events'
import { commandStatusText, waitCommand } from '@/composables/command'
import { bytes, chainStatusText, datetime, errorCodeText, goDuration, jobDuration, rate, statusText, triggerText, typeText } from '@/format'
import type { Command, JobDetail, JobEvent, Progress } from '@/types'
import JobStatus from './JobStatus.vue'
import { ChatDotRound } from '@element-plus/icons-vue'
import { ask, enabled as assistantEnabled, pageContext } from '@/composables/assistant'
import { t } from '@/i18n'

// JobDrawer shows everything about a job (PRD J-3, J-4, J-7) and offers
// cancel, complete log and restore guide.
const props = defineProps<{ jobId: string | null }>()
const emit = defineEmits<{ close: []; restore: [string]; open: [string] }>()

const job = ref<JobDetail | null>(null)
const loading = ref(false)
const progress = ref<Progress | null>(null)

async function load() {
  if (!props.jobId) return
  loading.value = true
  try {
    job.value = await api.get<JobDetail>(`/jobs/${props.jobId}`)
    const last = [...job.value.events].reverse().find((e) => e.type === 'progress')
    progress.value = (last?.payload as unknown as Progress) ?? null
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}

useEvents('job', (u) => {
  if (!job.value || u.job.id !== job.value.id) return
  if (u.progress) progress.value = u.progress
  if (u.job.status !== job.value.status) load()
})

// The assistant knows which job is open.
watch(
  () => props.jobId,
  (id) => {
    if (id) pageContext.job_id = id
    else delete pageContext.job_id
  },
  { immediate: true },
)
onBeforeUnmount(() => delete pageContext.job_id)

function askAssistant() {
  const j = job.value
  if (!j) return
  let q = t('jobDrawer.askStatus')
  if (j.status === 'failed' || j.status === 'canceled') q = t('jobDrawer.askFailed')
  else if (restorable.value) q = t('jobDrawer.askRestore')
  ask(q, { job_id: j.id })
  emit('close') // the drawer would cover the assistant
}

const running = computed(() => job.value?.status === 'running' || job.value?.status === 'pending')
const restorable = computed(() => job.value && (job.value.status === 'success' || job.value.status === 'warning') && job.value.chain_id)
const speed = computed(() => {
  const j = job.value
  if (!j?.started_at || !j.finished_at || !j.compressed_size) return null
  const sec = (new Date(j.finished_at).getTime() - new Date(j.started_at).getTime()) / 1000
  return sec > 0 ? j.compressed_size / sec : null
})

async function cancel() {
  await ElMessageBox.confirm(t('jobDrawer.cancelConfirm'), t('jobDrawer.cancel'), { type: 'warning', confirmButtonText: t('jobDrawer.cancel') })
  try {
    const c = await api.post<Command>(`/jobs/${props.jobId}/cancel`)
    const done = await waitCommand(c.id, 60_000)
    if (done.status === 'done') ElMessage.success(done.result?.message === 'canceled' ? t('status.canceled') : (done.result?.message ?? t('commandStatus.done')))
    else ElMessage.warning(done.result?.message ?? commandStatusText[done.status])
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

// Complete log (PRD J-4): uploaded by the agent on request.
const log = ref<string | null>(null)
const logLoading = ref(false)
async function showLog() {
  logLoading.value = true
  try {
    if (!job.value?.log_uploaded_at) {
      const c = await api.post<Command>(`/jobs/${props.jobId}/log/request`)
      const done = await waitCommand(c.id, 120_000)
      if (done.status !== 'done') {
        ElMessage.warning(done.result?.message ?? commandStatusText[done.status])
        return
      }
      await load()
    }
    log.value = await api.get<string>(`/jobs/${props.jobId}/log`)
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    logLoading.value = false
  }
}

const eventText = (e: JobEvent) => {
  const p = e.payload as Record<string, unknown>
  switch (e.type) {
    case 'status':
      return p.error_code
        ? t('jobDrawer.eventStatusCode', { status: statusText[p.status as string] ?? p.status, code: errorCodeText[p.error_code as string] ?? p.error_code })
        : t('jobDrawer.eventStatus', { status: statusText[p.status as string] ?? p.status })
    case 'progress':
      return t('jobDrawer.eventProgress', {
        written: bytes(p.compressed_bytes as number),
        rate: rate(p.bytes_per_second as number),
        elapsed: goDuration(p.elapsed as string),
      })
    case 'cleanup':
      return t('jobDrawer.eventCleanup', { dir: p.dir, reason: p.reason })
    default:
      return `${e.type}${t('common.colon')}${JSON.stringify(p)}`
  }
}
// Progress samples every 5s would flood the timeline; keep status changes,
// cleanups and the newest progress sample.
const timeline = computed(() => {
  const events = job.value?.events ?? []
  const lastProgress = events.findLastIndex((e) => e.type === 'progress')
  return events.filter((e, i) => e.type !== 'progress' || i === lastProgress)
})
const eventColor: Record<string, string> = {
  status: 'var(--el-color-primary)',
  progress: 'var(--el-border-color)',
  cleanup: 'var(--el-color-warning)',
  message: 'var(--el-color-info)',
}

watch(
  () => props.jobId,
  () => {
    job.value = null
    log.value = null
    load()
  },
  { immediate: true },
)
</script>

<template>
  <el-drawer :model-value="!!jobId" size="min(900px, 92vw)" @close="emit('close')">
    <template #header>
      <span v-if="job">
        <b>{{ job.project }}</b> {{ $t('common.typeBackup', { type: typeText[job.type] }) }} <JobStatus :status="job.status" :code="job.error_code" />
      </span>
    </template>
    <div v-loading="loading">
      <template v-if="job">
        <div class="actions">
          <el-button v-if="running" type="danger" plain @click="cancel">{{ $t('jobDrawer.cancel') }}</el-button>
          <el-button v-if="restorable" type="primary" @click="emit('restore', job.id)">{{ $t('jobDrawer.restoreGuide') }}</el-button>
          <el-button v-if="job.path" :loading="logLoading" @click="showLog">{{ job.log_uploaded_at ? $t('jobDrawer.viewLog') : $t('jobDrawer.fetchLog') }}</el-button>
          <el-button v-if="assistantEnabled" :icon="ChatDotRound" @click="askAssistant">{{ $t('jobDrawer.ask') }}</el-button>
        </div>

        <el-alert v-if="job.error_code || job.error_message" type="error" :closable="false" show-icon class="block"
          :title="errorCodeText[job.error_code ?? ''] ?? job.error_code ?? $t('status.failed')" :description="job.error_message" />

        <el-card v-if="job.status === 'running' && progress" shadow="never" class="block">
          {{ $t('progress.written') }} <b>{{ bytes(progress.compressed_bytes) }}</b> · {{ rate(progress.bytes_per_second) }} · {{ $t('progress.elapsed', { d: goDuration(progress.elapsed) }) }}
        </el-card>

        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item :label="$t('job.id')"><span class="mono">{{ job.id }}</span></el-descriptions-item>
          <el-descriptions-item :label="$t('job.host')">{{ job.host_name }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.trigger')">{{ triggerText[job.trigger] }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.scheduledAt')">{{ datetime(job.scheduled_at) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.startedAt')">{{ datetime(job.started_at) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.finishedAt')">{{ datetime(job.finished_at) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.duration')">{{ jobDuration(job.started_at, job.finished_at) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.speed')">{{ rate(speed) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.compressedSize')">{{ bytes(job.compressed_size) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('job.rawSize')">{{ bytes(job.raw_size) }}</el-descriptions-item>
          <el-descriptions-item label="LSN">
            <span class="mono">{{ job.from_lsn ?? 0 }} → {{ job.to_lsn ?? '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="changed page bitmap">
            <template v-if="job.type === 'incr' && job.status !== 'failed'">
              <el-tag :type="job.used_bitmap ? 'success' : 'warning'" size="small">{{ job.used_bitmap ? $t('job.bitmapUsed') : $t('job.bitmapUnused') }}</el-tag>
            </template>
            <span v-else class="muted">-</span>
          </el-descriptions-item>
          <el-descriptions-item :label="$t('job.masterPos')" :span="2">
            <span v-if="job.slave_info?.master_log_file" class="mono">{{ job.slave_info.master_log_file }}:{{ job.slave_info.master_log_pos }}</span>
            <span v-else class="muted">-</span>
            <span v-if="job.slave_info?.gtid_executed" class="mono muted"> GTID {{ job.slave_info.gtid_executed }}</span>
          </el-descriptions-item>
          <el-descriptions-item :label="$t('job.chain')" :span="2">
            <template v-if="job.chain">
              <span class="mono">{{ job.chain.id }}</span>
              <el-tag size="small" class="ml">{{ chainStatusText[job.chain.status] ?? job.chain.status }}</el-tag>
            </template>
            <span v-else class="muted">-</span>
          </el-descriptions-item>
          <el-descriptions-item :label="$t('job.base')" :span="2">
            <el-link v-if="job.base" type="primary" @click="emit('open', job.base.id)">
              {{ typeText[job.base.type] }} · {{ datetime(job.base.finished_at) }}
            </el-link>
            <span v-else class="muted">{{ job.type === 'full' ? $t('job.fullNoBase') : '-' }}</span>
          </el-descriptions-item>
          <el-descriptions-item :label="$t('job.file')" :span="2"><span class="mono">{{ job.path || '-' }}</span></el-descriptions-item>
        </el-descriptions>

        <h4>{{ $t('job.logTail') }}</h4>
        <pre v-if="job.log_tail" class="code log">{{ job.log_tail }}</pre>
        <p v-else class="muted">{{ $t('common.none') }}</p>

        <h4>{{ $t('job.events') }}</h4>
        <el-timeline>
          <el-timeline-item v-for="e in timeline" :key="e.seq" :timestamp="datetime(e.at)" :color="eventColor[e.type]" placement="top">
            {{ eventText(e) }}
          </el-timeline-item>
        </el-timeline>
        <p v-if="!timeline.length" class="muted">{{ $t('common.none') }}</p>
      </template>
    </div>

    <el-dialog :model-value="log !== null" :title="$t('jobDrawer.fullLog')" width="90%" top="4vh" append-to-body @close="log = null">
      <pre class="code full-log">{{ log }}</pre>
    </el-dialog>
  </el-drawer>
</template>

<style scoped>
.actions {
  margin-bottom: 16px;
}
.block {
  margin-bottom: 16px;
}
.ml {
  margin-left: 6px;
}
h4 {
  margin: 16px 0 8px;
}
.log {
  max-height: 360px;
}
.full-log {
  max-height: 75vh;
}
</style>
