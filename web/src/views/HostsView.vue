<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { useEvents } from '@/composables/events'
import { commandStatusText, waitCommand } from '@/composables/command'
import { ago, bytes, datetime, typeText } from '@/format'
import type { Command, Host, RegisterToken } from '@/types'
import HostStatus from '@/components/HostStatus.vue'
import HostCard from '@/components/HostCard.vue'

const route = useRoute()
const router = useRouter()
const hosts = ref<Host[]>([])
const tokens = ref<RegisterToken[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    ;[hosts.value, tokens.value] = await Promise.all([api.get<Host[]>('/hosts'), api.get<RegisterToken[]>('/register-tokens')])
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)

useEvents('host', (h) => {
  const i = hosts.value.findIndex((x) => x.id === h.id)
  if (i >= 0) hosts.value[i] = h
  else load()
})

function maxDisk(h: Host) {
  const disks = h.metrics?.disks ?? []
  if (!disks.length) return null
  return Math.max(...disks.map((d) => (d.total ? (d.total - d.free) / d.total : 0)))
}

// Detail drawer, opened with ?id=.
const selected = computed(() => hosts.value.find((h) => String(h.id) === route.query.id))
const drawer = computed({
  get: () => !!selected.value,
  set: (v) => {
    if (!v) router.replace({ query: { ...route.query, id: undefined } })
  },
})
function open(h: Host) {
  router.replace({ query: { ...route.query, id: h.id } })
}

const bitmapAlert = 10 * 2 ** 30

async function setPaused(h: Host, paused: boolean) {
  if (paused) {
    await ElMessageBox.confirm(
      `暂停后 ${h.name} 不再按计划排队备份（手动执行和已排队的任务仍会执行），暂停期间错过的计划点不会补跑。`,
      '暂停调度',
      { type: 'warning', confirmButtonText: '暂停' },
    )
  }
  try {
    const updated = await api.patch<Host>(`/hosts/${h.id}`, { paused })
    Object.assign(h, updated)
    ElMessage.success(paused ? '已暂停调度，agent 下次心跳时生效' : '已恢复调度')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

async function revoke(h: Host) {
  await ElMessageBox.confirm(
    `吊销后 ${h.name} 的 agent 无法再上报或获取配置，但会按最后一次配置继续备份。重新接入需要新的注册 token。`,
    '吊销主机',
    { type: 'error', confirmButtonText: '吊销', confirmButtonClass: 'el-button--danger' },
  )
  try {
    Object.assign(h, await api.post<Host>(`/hosts/${h.id}/revoke`))
    ElMessage.success('已吊销')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

const rescanning = ref(false)
async function rescan(h: Host) {
  rescanning.value = true
  try {
    const c = await api.post<Command>(`/hosts/${h.id}/rescan`)
    const done = await waitCommand(c.id, 30_000)
    if (done.status === 'done') {
      ElMessage.success('容器列表已刷新：' + (done.result?.message ?? ''))
      setTimeout(load, 1000)
    } else {
      ElMessage.warning(commandStatusText[done.status] ?? done.status)
    }
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    rescanning.value = false
  }
}

// Registration tokens (PRD H-1, 10.3).
const tokenDialog = ref(false)
const tokenForm = reactive({ note: '', ttl: '24h' })
const created = ref<RegisterToken | null>(null)
async function createToken() {
  try {
    created.value = await api.post<RegisterToken>('/register-tokens', tokenForm)
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}
function openTokenDialog() {
  created.value = null
  tokenForm.note = ''
  tokenForm.ttl = '24h'
  tokenDialog.value = true
}
async function deleteToken(t: RegisterToken) {
  try {
    await api.del(`/register-tokens/${t.id}`)
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('浏览器不允许复制，请手动选择')
  }
}
const agentURL = computed(() => `https://${location.hostname}:<agent_listen 端口>`)
const install = computed(
  () => `install -d -m 700 /etc/backup-agent /etc/backup-agent/tls
echo '${created.value?.token ?? ''}' > /etc/backup-agent/register.token
chmod 600 /etc/backup-agent/register.token
# 拷贝 server 的 ca.crt 到 /etc/backup-agent/tls/ca.crt
# 在 /etc/backup-agent/agent.yaml 中设置 server_url: ${agentURL.value}
systemctl enable --now backup-agent`,
)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>主机</h2>
      <div>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openTokenDialog">接入新主机</el-button>
      </div>
    </div>

    <el-table v-loading="loading" :data="hosts" @row-click="open" class="clickable">
      <el-table-column label="名称" min-width="140">
        <template #default="{ row }"><b>{{ row.name }}</b></template>
      </el-table-column>
      <el-table-column label="状态" min-width="160">
        <template #default="{ row }"><HostStatus :status="row.status" :paused="row.paused" :compatible="row.compatible" /></template>
      </el-table-column>
      <el-table-column prop="ip" label="IP" min-width="120" />
      <el-table-column prop="agent_version" label="agent 版本" min-width="100" />
      <el-table-column label="最后心跳" min-width="110">
        <template #default="{ row }">
          <el-tooltip :content="datetime(row.last_heartbeat_at)"><span>{{ ago(row.last_heartbeat_at) }}</span></el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="当前任务" min-width="180">
        <template #default="{ row }">
          <span v-if="row.state?.current" :class="{ muted: row.status !== 'online' }">
            {{ row.state.current.project }} {{ typeText[row.state.current.type] }}
            <template v-if="row.state.progress">· {{ bytes(row.state.progress.compressed_bytes) }}</template>
          </span>
          <span v-else class="muted">空闲</span>
          <span v-if="row.state?.queue?.length" class="muted">（排队 {{ row.state.queue.length }}）</span>
        </template>
      </el-table-column>
      <el-table-column label="磁盘最高使用率" min-width="120">
        <template #default="{ row }">
          <span v-if="maxDisk(row) !== null" :class="{ danger: maxDisk(row)! > 0.9 }">{{ (maxDisk(row)! * 100).toFixed(1) }}%</span>
          <span v-else class="muted">-</span>
        </template>
      </el-table-column>
      <el-table-column label="配置" min-width="100">
        <template #default="{ row }">
          <el-tag v-if="row.applied_config_version >= row.config_version" type="success" size="small">已生效</el-tag>
          <el-tooltip v-else :content="`最新 v${row.config_version}，agent 已生效 v${row.applied_config_version}`">
            <el-tag type="warning" size="small">等待生效</el-tag>
          </el-tooltip>
        </template>
      </el-table-column>
    </el-table>

    <el-card v-if="tokens.length" shadow="never" class="tokens">
      <template #header>未使用的注册 token</template>
      <el-table :data="tokens" size="small">
        <el-table-column prop="note" label="备注" />
        <el-table-column label="创建时间"><template #default="{ row }">{{ datetime(row.created_at) }}</template></el-table-column>
        <el-table-column label="过期时间"><template #default="{ row }">{{ datetime(row.expires_at) }}</template></el-table-column>
        <el-table-column width="90">
          <template #default="{ row }">
            <el-popconfirm title="删除这个注册 token？" @confirm="deleteToken(row)">
              <template #reference><el-button link type="danger">删除</el-button></template>
            </el-popconfirm>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-drawer v-model="drawer" :title="selected?.name" size="min(960px, 90vw)">
      <template v-if="selected">
        <div class="actions">
          <el-button v-if="!selected.paused" :disabled="!!selected.revoked_at" @click="setPaused(selected, true)">暂停调度</el-button>
          <el-button v-else type="success" @click="setPaused(selected, false)">恢复调度</el-button>
          <el-button :loading="rescanning" :disabled="selected.status !== 'online'" @click="rescan(selected)">重新扫描容器</el-button>
          <el-button type="danger" plain :disabled="!!selected.revoked_at" @click="revoke(selected)">吊销</el-button>
        </div>

        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="状态"><HostStatus :status="selected.status" :paused="selected.paused" :compatible="selected.compatible" /></el-descriptions-item>
          <el-descriptions-item label="主机名">{{ selected.hostname }}</el-descriptions-item>
          <el-descriptions-item label="IP">{{ selected.ip }}</el-descriptions-item>
          <el-descriptions-item label="agent 版本">{{ selected.agent_version }}（API v{{ selected.api_version }}）</el-descriptions-item>
          <el-descriptions-item label="备份根目录"><span class="mono">{{ selected.backup_root || '-' }}</span></el-descriptions-item>
          <el-descriptions-item label="最后心跳">{{ datetime(selected.last_heartbeat_at) }}</el-descriptions-item>
          <el-descriptions-item label="配置版本">最新 v{{ selected.config_version }} / 已生效 v{{ selected.applied_config_version }}</el-descriptions-item>
          <el-descriptions-item label="接入时间">{{ datetime(selected.created_at) }}</el-descriptions-item>
          <el-descriptions-item v-if="selected.revoked_at" label="吊销时间">{{ datetime(selected.revoked_at) }}</el-descriptions-item>
        </el-descriptions>

        <HostCard :host="selected" :queue-limit="20" class="block" />

        <h4>changed page bitmap</h4>
        <el-table :data="selected.metrics?.bitmaps ?? []" size="small" empty-text="未上报" class="block">
          <el-table-column prop="project" label="项目" />
          <el-table-column label="大小">
            <template #default="{ row }"><span :class="{ danger: row.bytes > bitmapAlert }">{{ bytes(row.bytes) }}</span></template>
          </el-table-column>
          <el-table-column label="检查时间"><template #default="{ row }">{{ datetime(row.checked_at) }}</template></el-table-column>
        </el-table>

        <h4>容器（{{ selected.containers.length }}）</h4>
        <el-table :data="selected.containers" size="small" empty-text="未上报">
          <el-table-column prop="name" label="名称" min-width="160" />
          <el-table-column prop="image" label="镜像" min-width="160" show-overflow-tooltip />
          <el-table-column label="状态" width="80">
            <template #default="{ row }"><el-tag :type="row.running ? 'success' : 'info'" size="small">{{ row.state }}</el-tag></template>
          </el-table-column>
          <el-table-column label="端口" min-width="150">
            <template #default="{ row }"><div v-for="p in row.ports" :key="p" class="mono">{{ p }}</div></template>
          </el-table-column>
          <el-table-column label="挂载" min-width="220">
            <template #default="{ row }">
              <div v-for="m in row.mounts" :key="m.destination" class="mono wrap">{{ m.source }} → {{ m.destination }}</div>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-drawer>

    <el-dialog v-model="tokenDialog" title="接入新主机" width="640px">
      <template v-if="!created">
        <el-form label-width="90px" @submit.prevent="createToken">
          <el-form-item label="备注"><el-input v-model="tokenForm.note" placeholder="例如 backup-inx-01" maxlength="200" /></el-form-item>
          <el-form-item label="有效期">
            <el-radio-group v-model="tokenForm.ttl">
              <el-radio-button value="1h">1 小时</el-radio-button>
              <el-radio-button value="24h">24 小时</el-radio-button>
              <el-radio-button value="168h">7 天</el-radio-button>
            </el-radio-group>
          </el-form-item>
        </el-form>
        <p class="muted">注册 token 只能使用一次，agent 用它换取长期 token 后自动登记主机（同名主机会沿用原记录）。</p>
      </template>
      <template v-else>
        <el-alert title="token 只显示这一次，请立即保存" type="warning" show-icon :closable="false" />
        <div class="token">
          <code class="mono">{{ created.token }}</code>
          <el-button size="small" @click="copy(created.token!)">复制</el-button>
        </div>
        <p>过期时间：{{ datetime(created.expires_at) }}。在新主机上执行：</p>
        <pre class="code">{{ install }}</pre>
        <p class="muted">agent 注册后主机出现在列表中，确认在线后到“项目”页面添加项目。</p>
      </template>
      <template #footer>
        <el-button v-if="!created" @click="tokenDialog = false">取消</el-button>
        <el-button v-if="!created" type="primary" @click="createToken">生成</el-button>
        <el-button v-else type="primary" @click="tokenDialog = false">完成</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.clickable :deep(.el-table__row) {
  cursor: pointer;
}
.tokens {
  margin-top: 20px;
}
.actions {
  margin-bottom: 16px;
}
.block {
  margin-bottom: 16px;
}
h4 {
  margin: 16px 0 8px;
}
.wrap {
  word-break: break-all;
}
.token {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 16px 0;
  padding: 10px;
  background: var(--el-fill-color-light);
  border-radius: 4px;
  word-break: break-all;
}
</style>
