<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
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
import PageHeader from '@/components/PageHeader.vue'
import { pageContext } from '@/composables/assistant'
import { t } from '@/i18n'

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

const onlineCount = computed(() => hosts.value.filter((h) => h.status === 'online').length)

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
watch(
  () => selected.value?.id,
  (id) => {
    if (id) pageContext.host_id = id
    else delete pageContext.host_id
  },
  { immediate: true },
)
onBeforeUnmount(() => delete pageContext.host_id)

function open(h: Host) {
  router.replace({ query: { ...route.query, id: h.id } })
}

const bitmapAlert = 10 * 2 ** 30

async function setPaused(h: Host, paused: boolean) {
  if (paused) {
    await ElMessageBox.confirm(
      t('hosts.pauseConfirm', { name: h.name }),
      t('hosts.pause'),
      { type: 'warning', confirmButtonText: t('hosts.pauseButton') },
    )
  }
  try {
    const updated = await api.patch<Host>(`/hosts/${h.id}`, { paused })
    Object.assign(h, updated)
    ElMessage.success(paused ? t('hosts.paused') : t('hosts.resumed'))
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

async function revoke(h: Host) {
  await ElMessageBox.confirm(
    t('hosts.revokeConfirm', { name: h.name }),
    t('hosts.revokeTitle'),
    { type: 'error', confirmButtonText: t('hosts.revoke'), confirmButtonClass: 'el-button--danger' },
  )
  try {
    Object.assign(h, await api.post<Host>(`/hosts/${h.id}/revoke`))
    ElMessage.success(t('hosts.revoked'))
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
      ElMessage.success(t('hosts.rescanned', { msg: done.result?.message ?? '' }))
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
async function deleteToken(tok: RegisterToken) {
  try {
    await api.del(`/register-tokens/${tok.id}`)
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(t('common.copied'))
  } catch {
    ElMessage.warning(t('common.copyFailed'))
  }
}
const agentURL = computed(() => `https://${location.hostname}:<${t('hosts.agentListenPort')}>`)
const install = computed(
  () => `install -d -m 700 /etc/backup-agent /etc/backup-agent/tls
echo '${created.value?.token ?? ''}' > /etc/backup-agent/register.token
chmod 600 /etc/backup-agent/register.token
# ${t('hosts.installCopyCA')}
# ${t('hosts.installServerURL', { url: agentURL.value })}
systemctl enable --now backup-agent`,
)
</script>

<template>
  <div class="bk-page">
    <PageHeader :title="$t('nav.hosts')" :eyebrow="$t('nav.monitor')" :description="$t('hosts.description')">
      <template #actions>
        <el-button :icon="Refresh" @click="load">{{ $t('common.refresh') }}</el-button>
        <el-button type="primary" :icon="Plus" @click="openTokenDialog">{{ $t('hosts.add') }}</el-button>
      </template>
    </PageHeader>

    <div class="bk-panel">
      <div class="bk-panel__head">
        <div class="bk-panel__title">{{ $t('hosts.list') }}</div>
        <span class="bk-panel__subtitle">{{ $t('hosts.online', { n: onlineCount, total: hosts.length }) }}</span>
      </div>
    <el-table v-loading="loading" :data="hosts" @row-click="open" class="clickable">
      <el-table-column :label="$t('hosts.name')" min-width="140">
        <template #default="{ row }"><b>{{ row.name }}</b></template>
      </el-table-column>
      <el-table-column :label="$t('job.status')" min-width="160">
        <template #default="{ row }"><HostStatus :status="row.status" :paused="row.paused" :compatible="row.compatible" /></template>
      </el-table-column>
      <el-table-column label="IP" min-width="120"><template #default="{ row }"><span class="bk-data">{{ row.ip }}</span></template></el-table-column>
      <el-table-column :label="$t('hosts.agentVersion')" min-width="110"><template #default="{ row }"><span class="bk-data">{{ row.agent_version }}</span></template></el-table-column>
      <el-table-column :label="$t('hosts.lastHeartbeat')" min-width="120">
        <template #default="{ row }">
          <el-tooltip :content="datetime(row.last_heartbeat_at)"><span>{{ ago(row.last_heartbeat_at) }}</span></el-tooltip>
        </template>
      </el-table-column>
      <el-table-column :label="$t('hostCard.current')" min-width="180">
        <template #default="{ row }">
          <span v-if="row.state?.current" :class="{ muted: row.status !== 'online' }">
            {{ row.state.current.project }} {{ typeText[row.state.current.type] }}
            <template v-if="row.state.progress">· {{ bytes(row.state.progress.compressed_bytes) }}</template>
          </span>
          <span v-else class="muted">{{ $t('hostCard.idle') }}</span>
          <span v-if="row.state?.queue?.length" class="muted">{{ $t('hosts.queued', { n: row.state.queue.length }) }}</span>
        </template>
      </el-table-column>
      <el-table-column :label="$t('hosts.maxDisk')" min-width="130">
        <template #default="{ row }">
          <span v-if="maxDisk(row) !== null" class="bk-num" :class="{ danger: maxDisk(row)! > 0.9 }">{{ (maxDisk(row)! * 100).toFixed(1) }}%</span>
          <span v-else class="muted">-</span>
        </template>
      </el-table-column>
      <el-table-column :label="$t('hosts.config')" min-width="110">
        <template #default="{ row }">
          <el-tag v-if="row.applied_config_version >= row.config_version" type="success" size="small">{{ $t('hosts.applied') }}</el-tag>
          <el-tooltip v-else :content="$t('hosts.configPending', { latest: row.config_version, applied: row.applied_config_version })">
            <el-tag type="warning" size="small">{{ $t('hosts.pending') }}</el-tag>
          </el-tooltip>
        </template>
      </el-table-column>
    </el-table>
    </div>

    <div v-if="tokens.length" class="bk-panel">
      <div class="bk-panel__head">
        <div class="bk-panel__title">{{ $t('hosts.tokens') }}</div>
        <span class="bk-panel__subtitle">{{ $t('hosts.tokensHint') }}</span>
      </div>
      <el-table :data="tokens" size="small">
        <el-table-column prop="note" :label="$t('hosts.note')" />
        <el-table-column :label="$t('hosts.createdAt')"><template #default="{ row }">{{ datetime(row.created_at) }}</template></el-table-column>
        <el-table-column :label="$t('hosts.expiresAt')"><template #default="{ row }">{{ datetime(row.expires_at) }}</template></el-table-column>
        <el-table-column width="90">
          <template #default="{ row }">
            <el-popconfirm :title="$t('hosts.deleteToken')" @confirm="deleteToken(row)">
              <template #reference><el-button link type="danger">{{ $t('common.delete') }}</el-button></template>
            </el-popconfirm>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-drawer v-model="drawer" :title="selected?.name" size="min(960px, 90vw)">
      <template v-if="selected">
        <div class="actions">
          <el-button v-if="!selected.paused" :disabled="!!selected.revoked_at" @click="setPaused(selected, true)">{{ $t('hosts.pause') }}</el-button>
          <el-button v-else type="success" @click="setPaused(selected, false)">{{ $t('hosts.resume') }}</el-button>
          <el-button :loading="rescanning" :disabled="selected.status !== 'online'" @click="rescan(selected)">{{ $t('hosts.rescan') }}</el-button>
          <el-button type="danger" plain :disabled="!!selected.revoked_at" @click="revoke(selected)">{{ $t('hosts.revoke') }}</el-button>
        </div>

        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item :label="$t('job.status')"><HostStatus :status="selected.status" :paused="selected.paused" :compatible="selected.compatible" /></el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.hostname')">{{ selected.hostname }}</el-descriptions-item>
          <el-descriptions-item label="IP">{{ selected.ip }}</el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.agentVersion')">{{ selected.agent_version }}{{ $t('common.paren', { s: `API v${selected.api_version}` }) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.backupRoot')"><span class="mono">{{ selected.backup_root || '-' }}</span></el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.lastHeartbeat')">{{ datetime(selected.last_heartbeat_at) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.configVersion')">{{ $t('hosts.configVersions', { latest: selected.config_version, applied: selected.applied_config_version }) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('hosts.registeredAt')">{{ datetime(selected.created_at) }}</el-descriptions-item>
          <el-descriptions-item v-if="selected.revoked_at" :label="$t('hosts.revokedAt')">{{ datetime(selected.revoked_at) }}</el-descriptions-item>
        </el-descriptions>

        <HostCard :host="selected" :queue-limit="20" class="block" />

        <h4 class="section-label">changed page bitmap</h4>
        <el-table :data="selected.metrics?.bitmaps ?? []" size="small" :empty-text="$t('hosts.notReported')" class="block">
          <el-table-column prop="project" :label="$t('job.project')" />
          <el-table-column :label="$t('job.size')">
            <template #default="{ row }"><span :class="{ danger: row.bytes > bitmapAlert }">{{ bytes(row.bytes) }}</span></template>
          </el-table-column>
          <el-table-column :label="$t('hosts.checkedAt')"><template #default="{ row }">{{ datetime(row.checked_at) }}</template></el-table-column>
        </el-table>

        <h4 class="section-label">{{ $t('hosts.containers', { n: selected.containers.length }) }}</h4>
        <el-table :data="selected.containers" size="small" :empty-text="$t('hosts.notReported')">
          <el-table-column prop="name" :label="$t('hosts.name')" min-width="160" />
          <el-table-column prop="image" :label="$t('hosts.image')" min-width="160" show-overflow-tooltip />
          <el-table-column :label="$t('job.status')" width="90">
            <template #default="{ row }"><el-tag :type="row.running ? 'success' : 'info'" size="small">{{ row.state }}</el-tag></template>
          </el-table-column>
          <el-table-column :label="$t('hosts.ports')" min-width="150">
            <template #default="{ row }"><div v-for="p in row.ports" :key="p" class="mono">{{ p }}</div></template>
          </el-table-column>
          <el-table-column :label="$t('hosts.mounts')" min-width="220">
            <template #default="{ row }">
              <div v-for="m in row.mounts" :key="m.destination" class="mono wrap">{{ m.source }} → {{ m.destination }}</div>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-drawer>

    <el-dialog v-model="tokenDialog" :title="$t('hosts.add')" width="640px">
      <template v-if="!created">
        <el-form :label-width="$t('hosts.tokenLabelWidth')" @submit.prevent="createToken">
          <el-form-item :label="$t('hosts.note')"><el-input v-model="tokenForm.note" :placeholder="$t('hosts.notePlaceholder')" maxlength="200" /></el-form-item>
          <el-form-item :label="$t('hosts.ttl')">
            <el-radio-group v-model="tokenForm.ttl">
              <el-radio-button value="1h">{{ $t('hosts.hours', { n: 1 }) }}</el-radio-button>
              <el-radio-button value="24h">{{ $t('hosts.hours', { n: 24 }) }}</el-radio-button>
              <el-radio-button value="168h">{{ $t('hosts.days', { n: 7 }) }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
        </el-form>
        <p class="muted">{{ $t('hosts.tokenHelp') }}</p>
      </template>
      <template v-else>
        <el-alert :title="$t('hosts.tokenOnce')" type="warning" show-icon :closable="false" />
        <div class="token">
          <code class="mono">{{ created.token }}</code>
          <el-button size="small" @click="copy(created.token!)">{{ $t('common.copy') }}</el-button>
        </div>
        <p>{{ $t('hosts.tokenExpires', { time: datetime(created.expires_at) }) }}</p>
        <pre class="code">{{ install }}</pre>
        <p class="muted">{{ $t('hosts.afterRegister') }}</p>
      </template>
      <template #footer>
        <el-button v-if="!created" @click="tokenDialog = false">{{ $t('common.cancel') }}</el-button>
        <el-button v-if="!created" type="primary" @click="createToken">{{ $t('hosts.generate') }}</el-button>
        <el-button v-else type="primary" @click="tokenDialog = false">{{ $t('common.done') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.clickable :deep(.el-table__row) {
  cursor: pointer;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 20px;
}
.actions .el-button + .el-button {
  margin-left: 0;
}
.block {
  margin-bottom: 16px;
}
h4 {
  margin: 20px 0 8px;
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
  background: var(--bk-subtle);
  border: 1px solid var(--bk-border);
  border-radius: 8px;
  word-break: break-all;
}
</style>
