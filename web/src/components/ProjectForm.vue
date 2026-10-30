<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { api, errorMessage } from '@/api'
import { commandStatusText, waitCommand } from '@/composables/command'
import { duration, goDuration, parseDuration } from '@/format'
import type { Check, Command, Host, OffsetScore, Project, ProjectConfig, Tier } from '@/types'
import { keyed, t } from '@/i18n'

// ProjectForm creates or edits a project (PRD P-1 ~ P-5, P-9). Durations are
// edited in minutes and sent as Go duration strings.
const props = defineProps<{ hosts: Host[]; project?: Project | null; templates: Record<Tier, ProjectConfig> }>()
const emit = defineEmits<{ saved: [Project]; cancel: [] }>()

const editing = computed(() => !!props.project)
const formRef = ref<FormInstance>()
const saving = ref(false)

const min = (d?: string) => Math.round(parseDuration(d) / 60)
const dur = (minutes: number) => `${minutes}m`

const form = reactive({
  host_id: undefined as number | undefined,
  name: '',
  container: '',
  tier: 'important' as Tier,
  full: 240,
  incr: 60,
  offset: 0,
  incr_mode: 'chained',
  retention_days: 7,
  lag: 10,
  parallel: 4,
  throttle: 0,
  timeout: 360,
  retry_count: 1,
  retry_interval: 10,
  priority: 0,
  enabled: true,
  user: 'backup',
  password: '',
  socket: '',
  mysql_host: '',
  port: 0,
})

function applyConfig(p: ProjectConfig) {
  Object.assign(form, {
    tier: p.tier,
    full: min(p.full_interval),
    incr: min(p.incr_interval),
    offset: min(p.offset),
    incr_mode: p.incr_mode,
    retention_days: p.retention_days,
    lag: min(p.lag_threshold),
    parallel: p.parallel,
    throttle: p.throttle ?? 0,
    timeout: min(p.timeout),
    retry_count: p.retry_count,
    retry_interval: min(p.retry_interval),
  })
}

function reset() {
  const p = props.project
  if (p) {
    applyConfig(p)
    Object.assign(form, {
      host_id: p.host_id,
      name: p.name,
      container: p.container,
      priority: p.priority,
      enabled: !p.disabled,
      user: p.mysql.user,
      password: '',
      socket: p.mysql.socket ?? '',
      mysql_host: p.mysql.host ?? '',
      port: p.mysql.port ?? 0,
    })
  } else {
    applyConfig(props.templates.important)
    Object.assign(form, { host_id: props.hosts.find((h) => !h.revoked_at)?.id, name: '', container: '', priority: 0, enabled: true, password: '' })
  }
  checks.value = null
  scores.value = null
}

// Choosing a tier applies its template (PRD P-2).
function onTier(tier: string | number | boolean | undefined) {
  applyConfig(props.templates[tier as Tier])
}

const host = computed(() => props.hosts.find((h) => h.id === form.host_id))
const containers = computed(() => host.value?.containers ?? [])

function onContainer(name: string) {
  if (!form.name && !editing.value) form.name = name.replace(/_slave$/, '').replace(/_maindb$/, '')
}

const fullOptions = [60, 120, 180, 240, 360, 480, 720, 1440, 2880, 10080]
const incrOptions = computed(() => [0, 15, 30, 60, 120, 180, 240, 360, 720].filter((m) => m === 0 || (m < form.full && form.full % m === 0)))
watch(
  () => form.full,
  () => {
    if (!incrOptions.value.includes(form.incr)) form.incr = 0
    if (form.offset >= form.full) form.offset = 0
  },
)

const rules = computed<FormRules>(() => ({
  host_id: [{ required: true, message: t('projectForm.hostRequired') }],
  container: [{ required: true, message: t('projectForm.containerRequired') }],
  name: [
    { required: true, message: t('projectForm.nameRequired') },
    { pattern: /^[A-Za-z0-9][A-Za-z0-9_.-]*$/, message: t('projectForm.namePattern') },
  ],
  user: [{ required: true, message: t('projectForm.userRequired') }],
  password: [{ validator: (_r, v, cb) => (editing.value || v ? cb() : cb(new Error(t('login.passwordRequired')))) }],
}))

function body(): ProjectConfig & { host_id?: number } {
  return {
    host_id: form.host_id,
    name: form.name,
    container: form.container,
    tier: form.tier,
    full_interval: dur(form.full),
    incr_interval: dur(form.incr),
    offset: dur(form.offset),
    incr_mode: form.incr_mode as ProjectConfig['incr_mode'],
    retention_days: form.retention_days,
    lag_threshold: dur(form.lag),
    parallel: form.parallel,
    throttle: form.throttle,
    timeout: dur(form.timeout),
    retry_count: form.retry_count,
    retry_interval: dur(form.retry_interval),
    priority: form.priority,
    disabled: !form.enabled,
    mysql: {
      user: form.user,
      password: form.password || undefined,
      socket: form.socket || undefined,
      host: form.mysql_host || undefined,
      port: form.port || undefined,
    },
  }
}

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  saving.value = true
  try {
    const p = editing.value
      ? await api.put<Project>(`/projects/${props.project!.id}`, body())
      : await api.post<Project>('/projects', body())
    ElMessage.success(editing.value ? t('projectForm.saved') : t('projectForm.created'))
    emit('saved', p)
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    saving.value = false
  }
}

// Offset recommendation (PRD P-5).
const recommending = ref(false)
const scores = ref<OffsetScore[] | null>(null)
const best = ref(0)
async function recommend() {
  if (!form.host_id) return
  recommending.value = true
  try {
    const r = await api.post<{ offset: string; candidates: OffsetScore[] }>('/projects/recommend-offset', {
      ...body(),
      mysql: undefined,
      name: form.name || undefined,
      container: form.container || undefined,
    })
    best.value = min(r.offset)
    form.offset = best.value
    scores.value = r.candidates
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    recommending.value = false
  }
}
const topScores = computed(() =>
  [...(scores.value ?? [])]
    .sort((a, b) => a.overloaded_hours - b.overloaded_hours || parseDuration(a.total_wait) - parseDuration(b.total_wait))
    .slice(0, 6),
)

// Connection test (PRD P-9).
const testing = ref(false)
const checks = ref<Check[] | null>(null)
const testStatus = ref('')
async function testConnection() {
  if (!form.host_id || !form.container || !form.user) {
    ElMessage.warning(t('projectForm.testNeeds'))
    return
  }
  testing.value = true
  checks.value = null
  testStatus.value = ''
  try {
    const q = editing.value && !form.password ? { project_id: props.project!.id } : undefined
    const c = await api.post<Command>('/projects/test-connection', body(), q)
    const done = await waitCommand(c.id, 120_000)
    const data = done.result?.data as { checks?: Check[] } | undefined
    checks.value = data?.checks ?? []
    testStatus.value = done.status === 'done' || done.status === 'failed' ? (done.result?.message ?? '') : commandStatusText[done.status]
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    testing.value = false
  }
}

const checkNames = keyed('projectForm.checks')

function hhmm(m: number) {
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`
}

watch(() => props.project, reset, { immediate: true })
</script>

<template>
  <el-form
    ref="formRef"
    :model="form"
    :rules="rules"
    :label-width="$t('projectForm.labelWidth')"
    :style="{ '--label-width': $t('projectForm.labelWidth') }"
    class="project-form"
  >
    <el-divider content-position="left">{{ $t('projectForm.basic') }}</el-divider>
    <el-form-item :label="$t('job.host')" prop="host_id">
      <el-select v-model="form.host_id" :disabled="editing" :placeholder="$t('schedule.selectHost')">
        <el-option v-for="h in hosts.filter((h) => !h.revoked_at)" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
    </el-form-item>
    <el-form-item :label="$t('projects.container')" prop="container">
      <el-select v-model="form.container" filterable :placeholder="$t('projectForm.containerPlaceholder')" @change="onContainer">
        <el-option v-for="c in containers" :key="c.name" :label="c.name" :value="c.name">
          <span>{{ c.name }}</span>
          <span class="muted option-extra">{{ c.image }} · {{ c.state }}</span>
        </el-option>
      </el-select>
      <span v-if="host && !containers.length" class="muted hint">{{ $t('projectForm.noContainers') }}</span>
    </el-form-item>
    <el-form-item :label="$t('projectForm.name')" prop="name">
      <el-input v-model="form.name" :disabled="editing" maxlength="64" />
      <div class="muted hint">{{ $t('projectForm.nameHint') }}</div>
    </el-form-item>
    <el-form-item :label="$t('projectForm.tier')">
      <el-radio-group v-model="form.tier" @change="onTier">
        <el-radio-button value="important">{{ $t('projectForm.tierImportant') }}</el-radio-button>
        <el-radio-button value="normal">{{ $t('projectForm.tierNormal') }}</el-radio-button>
      </el-radio-group>
    </el-form-item>

    <el-divider content-position="left">{{ $t('nav.schedule') }}</el-divider>
    <el-form-item :label="$t('projectForm.fullInterval')">
      <el-select v-model="form.full" class="short">
        <el-option v-for="m in fullOptions" :key="m" :label="duration(m * 60)" :value="m" />
      </el-select>
    </el-form-item>
    <el-form-item :label="$t('projectForm.incrInterval')">
      <el-select v-model="form.incr" class="short">
        <el-option v-for="m in incrOptions" :key="m" :label="m ? duration(m * 60) : $t('projectForm.noIncr')" :value="m" />
      </el-select>
    </el-form-item>
    <el-form-item :label="$t('projectForm.offset')">
      <el-input-number v-model="form.offset" :min="0" :max="form.full - 1" :step="15" class="short" />
      <span class="unit">{{ $t('projectForm.minutes') }}{{ $t('common.paren', { s: hhmm(form.offset) }) }}</span>
      <el-button :loading="recommending" :disabled="!form.host_id" @click="recommend">{{ $t('projectForm.recommend') }}</el-button>
    </el-form-item>
    <el-form-item v-if="scores" label="">
      <div class="scores">
        <div class="muted">{{ $t('projectForm.scoresHint') }}</div>
        <el-table :data="topScores" size="small" @row-click="(r: OffsetScore) => (form.offset = min(r.offset))" class="clickable">
          <el-table-column :label="$t('projectForm.offset')">
            <template #default="{ row }">
              {{ hhmm(min(row.offset)) }}
              <el-tag v-if="min(row.offset) === best" size="small" type="success">{{ $t('projectForm.recommended') }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="$t('projectForm.overloadedHours')" prop="overloaded_hours" />
          <el-table-column :label="$t('projectForm.peakLoad')"><template #default="{ row }">{{ goDuration(row.max_hour_load) }}</template></el-table-column>
          <el-table-column :label="$t('projectForm.totalWait')"><template #default="{ row }">{{ goDuration(row.total_wait) }}</template></el-table-column>
        </el-table>
      </div>
    </el-form-item>
    <el-form-item v-if="form.incr" :label="$t('projectForm.incrMode')">
      <el-radio-group v-model="form.incr_mode">
        <el-radio value="chained">{{ $t('projectForm.chained') }}</el-radio>
        <el-radio value="differential">{{ $t('projectForm.differential') }}</el-radio>
      </el-radio-group>
    </el-form-item>
    <el-form-item :label="$t('projectForm.priority')">
      <el-input-number v-model="form.priority" :min="0" :max="100" class="short" />
      <span class="unit muted">{{ $t('projectForm.priorityHint') }}</span>
    </el-form-item>

    <el-divider content-position="left">{{ $t('projectForm.execution') }}</el-divider>
    <div class="grid">
      <el-form-item :label="$t('projectForm.retentionDays')">
        <el-input-number v-model="form.retention_days" :min="1" :max="3650" />
      </el-form-item>
      <el-form-item :label="$t('projectForm.lag')">
        <el-input-number v-model="form.lag" :min="0" :max="1440" /><span class="unit">{{ $t('projectForm.minutes') }}</span>
      </el-form-item>
      <el-form-item label="--parallel">
        <el-input-number v-model="form.parallel" :min="1" :max="64" />
      </el-form-item>
      <el-form-item label="--throttle">
        <el-input-number v-model="form.throttle" :min="0" :max="100000" /><span class="unit">{{ $t('projectForm.unlimited') }}</span>
      </el-form-item>
      <el-form-item :label="$t('projectForm.timeout')">
        <el-input-number v-model="form.timeout" :min="1" :max="10080" :step="30" /><span class="unit">{{ $t('projectForm.minutes') }}</span>
      </el-form-item>
      <el-form-item :label="$t('projectForm.retries')">
        <el-input-number v-model="form.retry_count" :min="0" :max="10" /><span class="unit">{{ $t('projectForm.times') }}</span>
      </el-form-item>
      <el-form-item :label="$t('projectForm.retryInterval')">
        <el-input-number v-model="form.retry_interval" :min="1" :max="1440" /><span class="unit">{{ $t('projectForm.minutes') }}</span>
      </el-form-item>
      <el-form-item :label="$t('projects.enabledCol')">
        <el-switch v-model="form.enabled" />
      </el-form-item>
    </div>

    <el-divider content-position="left">{{ $t('projectForm.account') }}</el-divider>
    <el-form-item :label="$t('login.username')" prop="user"><el-input v-model="form.user" autocomplete="off" class="medium" /></el-form-item>
    <el-form-item :label="$t('login.password')" prop="password">
      <el-input
        v-model="form.password"
        type="password"
        show-password
        autocomplete="new-password"
        class="medium"
        :placeholder="editing ? $t('projectForm.passwordKeep') : ''"
      />
      <span class="muted hint">{{ $t('projectForm.passwordHint') }}</span>
    </el-form-item>
    <el-collapse class="advanced">
      <el-collapse-item :title="$t('projectForm.connection')">
        <el-form-item label="socket"><el-input v-model="form.socket" class="medium" /></el-form-item>
        <el-form-item label="host"><el-input v-model="form.mysql_host" class="medium" /></el-form-item>
        <el-form-item label="port"><el-input-number v-model="form.port" :min="0" :max="65535" /></el-form-item>
      </el-collapse-item>
    </el-collapse>
    <el-form-item :label="$t('projectForm.test')">
      <el-button :loading="testing" @click="testConnection">{{ $t('projectForm.testButton') }}</el-button>
      <span v-if="testing" class="muted hint">{{ $t('projectForm.testing') }}</span>
    </el-form-item>
    <el-form-item v-if="checks" label="">
      <div class="checks">
        <div v-for="c in checks" :key="c.name" class="check">
          <el-tag :type="c.ok ? 'success' : 'danger'" size="small">{{ c.ok ? $t('projectForm.passed') : $t('status.failed') }}</el-tag>
          <b>{{ checkNames[c.name] ?? c.name }}</b>
          <span class="muted">{{ c.message }}</span>
        </div>
        <div v-if="!checks.length" class="muted">{{ testStatus }}</div>
      </div>
    </el-form-item>

    <div class="footer">
      <el-button @click="emit('cancel')">{{ $t('common.cancel') }}</el-button>
      <el-button type="primary" :loading="saving" @click="submit">{{ editing ? $t('common.save') : $t('common.create') }}</el-button>
    </div>
  </el-form>
</template>

<style scoped>
.short {
  width: 160px;
}
.medium {
  width: 280px;
}
.unit {
  margin: 0 8px;
}
.hint {
  margin-left: 8px;
  font-size: 12px;
}
.option-extra {
  float: right;
  font-size: 12px;
  margin-left: 16px;
}
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
}
.scores {
  width: 100%;
}
.clickable :deep(.el-table__row) {
  cursor: pointer;
}
.advanced {
  margin: 0 0 16px var(--label-width);
}
.checks {
  width: 100%;
}
.check {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 4px;
}
.footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>
