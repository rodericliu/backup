<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { api, errorMessage } from '@/api'
import { commandStatusText, waitCommand } from '@/composables/command'
import { duration, goDuration, parseDuration } from '@/format'
import type { Check, Command, Host, OffsetScore, Project, ProjectConfig, Tier } from '@/types'

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
function onTier(t: string | number | boolean | undefined) {
  applyConfig(props.templates[t as Tier])
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

const rules: FormRules = {
  host_id: [{ required: true, message: '请选择主机' }],
  container: [{ required: true, message: '请选择容器' }],
  name: [
    { required: true, message: '请输入项目名' },
    { pattern: /^[A-Za-z0-9][A-Za-z0-9_.-]*$/, message: '字母、数字、下划线、点、减号，不能以符号开头' },
  ],
  user: [{ required: true, message: '请输入备份账号' }],
  password: [{ validator: (_r, v, cb) => (editing.value || v ? cb() : cb(new Error('请输入密码'))) }],
}

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
    ElMessage.success(editing.value ? '已保存，agent 下次心跳时生效' : '已创建，agent 下次心跳时生效')
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
    ElMessage.warning('请先选择主机、容器并填写账号')
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

const checkNames: Record<string, string> = {
  container: '容器',
  tools: '工具',
  backup_root: '备份目录挂载',
  credentials: '临时凭据',
  replication: '账号与复制状态',
  changed_page_bitmap: 'changed page bitmap',
}

function hhmm(m: number) {
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`
}

watch(() => props.project, reset, { immediate: true })
</script>

<template>
  <el-form ref="formRef" :model="form" :rules="rules" label-width="130px" class="project-form">
    <el-divider content-position="left">基本信息</el-divider>
    <el-form-item label="主机" prop="host_id">
      <el-select v-model="form.host_id" :disabled="editing" placeholder="选择主机">
        <el-option v-for="h in hosts.filter((h) => !h.revoked_at)" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
    </el-form-item>
    <el-form-item label="容器" prop="container">
      <el-select v-model="form.container" filterable placeholder="从 agent 上报的容器中选择" @change="onContainer">
        <el-option v-for="c in containers" :key="c.name" :label="c.name" :value="c.name">
          <span>{{ c.name }}</span>
          <span class="muted option-extra">{{ c.image }} · {{ c.state }}</span>
        </el-option>
      </el-select>
      <span v-if="host && !containers.length" class="muted hint">该主机尚未上报容器，请在主机页面重新扫描</span>
    </el-form-item>
    <el-form-item label="项目名" prop="name">
      <el-input v-model="form.name" :disabled="editing" maxlength="64" />
      <div class="muted hint">备份目录 &lt;backup_root&gt;/&lt;项目名&gt;；从独立运行切换过来的项目必须沿用原名称。创建后不可修改。</div>
    </el-form-item>
    <el-form-item label="等级">
      <el-radio-group v-model="form.tier" @change="onTier">
        <el-radio-button value="important">重要（4h 全量 + 1h 增量）</el-radio-button>
        <el-radio-button value="normal">普通（24h 全量）</el-radio-button>
      </el-radio-group>
    </el-form-item>

    <el-divider content-position="left">排班</el-divider>
    <el-form-item label="全量周期">
      <el-select v-model="form.full" class="short">
        <el-option v-for="m in fullOptions" :key="m" :label="duration(m * 60)" :value="m" />
      </el-select>
    </el-form-item>
    <el-form-item label="增量周期">
      <el-select v-model="form.incr" class="short">
        <el-option v-for="m in incrOptions" :key="m" :label="m ? duration(m * 60) : '不做增量'" :value="m" />
      </el-select>
    </el-form-item>
    <el-form-item label="偏移量">
      <el-input-number v-model="form.offset" :min="0" :max="form.full - 1" :step="15" class="short" />
      <span class="unit">分钟（{{ hhmm(form.offset) }}）</span>
      <el-button :loading="recommending" :disabled="!form.host_id" @click="recommend">推荐</el-button>
    </el-form-item>
    <el-form-item v-if="scores" label="">
      <div class="scores">
        <div class="muted">按“超负荷小时数 → 排队等待 → 峰值负载”排序，耗时取近 14 天平均（无历史按全量 30 分钟、增量 5 分钟估算）：</div>
        <el-table :data="topScores" size="small" @row-click="(r: OffsetScore) => (form.offset = min(r.offset))" class="clickable">
          <el-table-column label="偏移量">
            <template #default="{ row }">
              {{ hhmm(min(row.offset)) }}
              <el-tag v-if="min(row.offset) === best" size="small" type="success">推荐</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="超负荷小时" prop="overloaded_hours" />
          <el-table-column label="峰值小时负载"><template #default="{ row }">{{ goDuration(row.max_hour_load) }}</template></el-table-column>
          <el-table-column label="总排队等待"><template #default="{ row }">{{ goDuration(row.total_wait) }}</template></el-table-column>
        </el-table>
      </div>
    </el-form-item>
    <el-form-item v-if="form.incr" label="增量模式">
      <el-radio-group v-model="form.incr_mode">
        <el-radio value="chained">链式（基于上一份，体积小且稳定）</el-radio>
        <el-radio value="differential">差异（基于本链全量，恢复只需全量 + 1 份）</el-radio>
      </el-radio-group>
    </el-form-item>
    <el-form-item label="优先级">
      <el-input-number v-model="form.priority" :min="0" :max="100" class="short" />
      <span class="unit muted">同一时间点数值小的先执行</span>
    </el-form-item>

    <el-divider content-position="left">执行参数</el-divider>
    <div class="grid">
      <el-form-item label="保留天数">
        <el-input-number v-model="form.retention_days" :min="1" :max="3650" />
      </el-form-item>
      <el-form-item label="复制延迟阈值">
        <el-input-number v-model="form.lag" :min="0" :max="1440" /><span class="unit">分钟</span>
      </el-form-item>
      <el-form-item label="--parallel">
        <el-input-number v-model="form.parallel" :min="1" :max="64" />
      </el-form-item>
      <el-form-item label="--throttle">
        <el-input-number v-model="form.throttle" :min="0" :max="100000" /><span class="unit">0 不限</span>
      </el-form-item>
      <el-form-item label="超时">
        <el-input-number v-model="form.timeout" :min="1" :max="10080" :step="30" /><span class="unit">分钟</span>
      </el-form-item>
      <el-form-item label="失败重试">
        <el-input-number v-model="form.retry_count" :min="0" :max="10" /><span class="unit">次</span>
      </el-form-item>
      <el-form-item label="重试间隔">
        <el-input-number v-model="form.retry_interval" :min="1" :max="1440" /><span class="unit">分钟</span>
      </el-form-item>
      <el-form-item label="启用">
        <el-switch v-model="form.enabled" />
      </el-form-item>
    </div>

    <el-divider content-position="left">MySQL 备份账号</el-divider>
    <el-form-item label="用户名" prop="user"><el-input v-model="form.user" autocomplete="off" class="medium" /></el-form-item>
    <el-form-item label="密码" prop="password">
      <el-input
        v-model="form.password"
        type="password"
        show-password
        autocomplete="new-password"
        class="medium"
        :placeholder="editing ? '留空表示不修改' : ''"
      />
      <span class="muted hint">加密保存，不会回显</span>
    </el-form-item>
    <el-collapse class="advanced">
      <el-collapse-item title="连接参数（可选，默认使用容器内 mysql 客户端的默认值）">
        <el-form-item label="socket"><el-input v-model="form.socket" class="medium" /></el-form-item>
        <el-form-item label="host"><el-input v-model="form.mysql_host" class="medium" /></el-form-item>
        <el-form-item label="port"><el-input-number v-model="form.port" :min="0" :max="65535" /></el-form-item>
      </el-collapse-item>
    </el-collapse>
    <el-form-item label="连接测试">
      <el-button :loading="testing" @click="testConnection">测试连接</el-button>
      <span v-if="testing" class="muted hint">agent 正在检查容器、工具、挂载、账号与复制状态…</span>
    </el-form-item>
    <el-form-item v-if="checks" label="">
      <div class="checks">
        <div v-for="c in checks" :key="c.name" class="check">
          <el-tag :type="c.ok ? 'success' : 'danger'" size="small">{{ c.ok ? '通过' : '失败' }}</el-tag>
          <b>{{ checkNames[c.name] ?? c.name }}</b>
          <span class="muted">{{ c.message }}</span>
        </div>
        <div v-if="!checks.length" class="muted">{{ testStatus }}</div>
      </div>
    </el-form-item>

    <div class="footer">
      <el-button @click="emit('cancel')">取消</el-button>
      <el-button type="primary" :loading="saving" @click="submit">{{ editing ? '保存' : '创建' }}</el-button>
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
  margin: 0 0 16px 130px;
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
