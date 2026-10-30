<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { commandStatusText, waitCommand } from '@/composables/command'
import { goDuration, parseDuration, typeText } from '@/format'
import type { Command, Host, JobType, Project, ProjectConfig, Tier } from '@/types'
import ProjectForm from '@/components/ProjectForm.vue'
import PageHeader from '@/components/PageHeader.vue'
import { t } from '@/i18n'

const router = useRouter()
const projects = ref<Project[]>([])
const hosts = ref<Host[]>([])
const templates = ref<Record<Tier, ProjectConfig> | null>(null)
const loading = ref(false)
const hostFilter = ref<number | undefined>()
const showDeleted = ref(false)

async function load() {
  loading.value = true
  try {
    ;[projects.value, hosts.value] = await Promise.all([
      api.get<Project[]>('/projects', { include_deleted: showDeleted.value || undefined }),
      api.get<Host[]>('/hosts'),
    ])
    templates.value ??= await api.get<Record<Tier, ProjectConfig>>('/project-templates')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)

const rows = computed(() => projects.value.filter((p) => !hostFilter.value || p.host_id === hostFilter.value))

// Create and edit (PRD P-1 ~ P-5).
const dialog = ref(false)
const editing = ref<Project | null>(null)
function create() {
  editing.value = null
  dialog.value = true
}
function edit(p: Project) {
  editing.value = p
  dialog.value = true
}
function saved() {
  dialog.value = false
  load()
}

// Enable and disable (PRD P-6).
async function toggle(p: Project, enabled: boolean) {
  try {
    Object.assign(p, await api.patch<Project>(`/projects/${p.id}`, { enabled }))
    ElMessage.success(enabled ? t('projects.enabled') : t('projects.disabled'))
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

// Manual backups (PRD P-8).
async function run(p: Project, type: JobType) {
  await ElMessageBox.confirm(
    t('projects.runConfirm', { name: p.name, type: typeText[type], host: p.host_name }),
    t('projects.runNow'),
    { confirmButtonText: t('projects.run') },
  )
  try {
    const c = await api.post<Command>(`/projects/${p.id}/run`, { type })
    const done = await waitCommand(c.id, 30_000)
    if (done.status === 'done') {
      const id = (done.result?.data as { job_id?: string } | undefined)?.job_id
      ElMessage.success({ message: t('projects.queued'), duration: 4000 })
      if (id) router.push({ name: 'jobs', query: { id } })
    } else if (done.status === 'failed') {
      ElMessage.error(done.result?.message ?? t('projects.runFailed'))
    } else {
      ElMessage.warning(t('projects.runPending', { status: commandStatusText[done.status] }))
    }
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

// Delete (PRD P-7).
const deleting = ref<Project | null>(null)
const deletePolicy = ref<'keep' | 'purge'>('keep')
const confirmName = ref('')
function askDelete(p: Project) {
  deleting.value = p
  deletePolicy.value = 'keep'
  confirmName.value = ''
}
async function doDelete() {
  const p = deleting.value!
  try {
    await api.del(`/projects/${p.id}`, { files: deletePolicy.value })
    ElMessage.success(deletePolicy.value === 'purge' ? t('projects.deletedPurge') : t('projects.deletedKeep'))
    deleting.value = null
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}
const deleteDialog = computed({
  get: () => !!deleting.value,
  set: (v) => {
    if (!v) deleting.value = null
  },
})

function offsetText(d: string) {
  const m = Math.round(parseDuration(d) / 60)
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`
}

function schedule(p: Project) {
  const full = goDuration(p.full_interval)
  return p.incr_interval && goDuration(p.incr_interval) !== '0s'
    ? t('projects.scheduleFullIncr', { full, incr: goDuration(p.incr_interval) })
    : t('projects.scheduleFull', { full })
}
</script>

<template>
  <div class="bk-page">
    <PageHeader :title="$t('nav.projects')" :eyebrow="$t('nav.backup')" :description="$t('projects.description')">
      <template #actions>
        <el-button :icon="Refresh" @click="load">{{ $t('common.refresh') }}</el-button>
        <el-button type="primary" :icon="Plus" :disabled="!hosts.length" @click="create">{{ $t('projects.add') }}</el-button>
      </template>
    </PageHeader>

    <div class="bk-panel">
    <div class="toolbar">
      <el-select v-model="hostFilter" clearable :placeholder="$t('projects.allHosts')" style="width: 200px">
        <el-option v-for="h in hosts" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
      <el-checkbox v-model="showDeleted" @change="load">{{ $t('projects.showDeleted') }}</el-checkbox>
      <span class="spacer" />
      <span class="muted count">{{ $t('projects.count', { n: rows.length }) }}</span>
    </div>

    <el-table v-loading="loading" :data="rows">
      <el-table-column :label="$t('job.project')" min-width="130">
        <template #default="{ row }">
          <b>{{ row.name }}</b>
          <el-tag v-if="row.tier === 'important'" size="small" type="danger" effect="plain" class="ml">{{ $t('projects.important') }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="host_name" :label="$t('job.host')" min-width="120" />
      <el-table-column :label="$t('projects.container')" min-width="160"><template #default="{ row }"><span class="bk-data">{{ row.container }}</span></template></el-table-column>
      <el-table-column :label="$t('nav.schedule')" min-width="170">
        <template #default="{ row }">
          {{ schedule(row) }}
          <div class="muted">{{ $t('projects.offset', { o: offsetText(row.offset) }) }} · {{ row.incr_mode === 'differential' ? $t('projects.differential') : $t('projects.chained') }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="$t('projects.retention')" width="100"><template #default="{ row }">{{ $t('projects.days', { n: row.retention_days }) }}</template></el-table-column>
      <el-table-column :label="$t('hosts.config')" width="104">
        <template #default="{ row }">
          <el-tag v-if="row.deleted_at" type="info" size="small">{{ $t('projects.deleted') }}</el-tag>
          <el-tag v-else-if="row.applied" type="success" size="small">{{ $t('hosts.applied') }}</el-tag>
          <el-tag v-else type="warning" size="small">{{ $t('hosts.pending') }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="$t('projects.enabledCol')" width="80">
        <template #default="{ row }">
          <el-switch :model-value="!row.disabled" :disabled="!!row.deleted_at" size="small" @change="(v: string | number | boolean) => toggle(row, v as boolean)" />
        </template>
      </el-table-column>
      <el-table-column :label="$t('projects.actions')" width="280" fixed="right">
        <template #default="{ row }">
          <template v-if="!row.deleted_at">
            <el-dropdown trigger="click" class="run" @command="(type: JobType) => run(row, type)">
              <el-button link type="primary">{{ $t('projects.runNow') }}</el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="full">{{ typeText.full }}</el-dropdown-item>
                  <el-dropdown-item command="incr" :disabled="!row.incr_interval || goDuration(row.incr_interval) === '0s'">{{ typeText.incr }}</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
            <el-button link type="primary" @click="edit(row)">{{ $t('common.edit') }}</el-button>
            <el-button link type="primary" @click="router.push({ name: 'jobs', query: { project_id: row.id, tab: 'chains' } })">{{ $t('job.chain') }}</el-button>
            <el-button link type="danger" @click="askDelete(row)">{{ $t('common.delete') }}</el-button>
          </template>
          <span v-else class="muted">{{ row.delete_policy === 'purge' ? $t('projects.filesPurged') : $t('projects.filesKept') }}{{ row.purged_at ? $t('common.paren', { s: $t('projects.purged') }) : '' }}</span>
        </template>
      </el-table-column>
    </el-table>
    </div>

    <el-dialog v-model="dialog" :title="editing ? $t('projects.editTitle', { name: editing.name }) : $t('projects.add')" width="860px" destroy-on-close top="4vh">
      <ProjectForm v-if="templates" :hosts="hosts" :project="editing" :templates="templates" @saved="saved" @cancel="dialog = false" />
    </el-dialog>

    <el-dialog v-model="deleteDialog" :title="$t('projects.deleteTitle', { name: deleting?.name })" width="520px">
      <p>{{ $t('projects.deleteIntro') }}</p>
      <el-radio-group v-model="deletePolicy" class="policy">
        <el-radio value="keep">{{ $t('projects.deleteKeep', { n: deleting?.retention_days }) }}</el-radio>
        <el-radio value="purge">{{ $t('projects.deletePurge') }}</el-radio>
      </el-radio-group>
      <template v-if="deletePolicy === 'purge'">
        <el-alert type="error" show-icon :closable="false" :title="$t('projects.purgeWarning')" />
        <i18n-t keypath="projects.confirmName" tag="p"><template #name><b>{{ deleting?.name }}</b></template></i18n-t>
        <el-input v-model="confirmName" />
      </template>
      <template #footer>
        <el-button @click="deleting = null">{{ $t('common.cancel') }}</el-button>
        <el-button type="danger" :disabled="deletePolicy === 'purge' && confirmName !== deleting?.name" @click="doDelete">{{ $t('common.delete') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.spacer {
  flex: 1;
}
.count {
  font-size: 0.8125rem;
}
.run {
  vertical-align: middle;
  margin-right: 12px;
}
.ml {
  margin-left: 6px;
}
.policy {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  margin-bottom: 16px;
}
</style>
