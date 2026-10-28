<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { commandStatusText, waitCommand } from '@/composables/command'
import { goDuration, parseDuration } from '@/format'
import type { Command, Host, JobType, Project, ProjectConfig, Tier } from '@/types'
import ProjectForm from '@/components/ProjectForm.vue'

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
    ElMessage.success(enabled ? '已启用' : '已停用，已有备份保留')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

// Manual backups (PRD P-8).
async function run(p: Project, type: JobType) {
  await ElMessageBox.confirm(
    `立即执行 ${p.name} 的${type === 'full' ? '全量' : '增量'}备份？任务进入 ${p.host_name} 的串行队列，不会打断正在运行的备份。`,
    '立即执行',
    { confirmButtonText: '执行' },
  )
  try {
    const c = await api.post<Command>(`/projects/${p.id}/run`, { type })
    const done = await waitCommand(c.id, 30_000)
    if (done.status === 'done') {
      const id = (done.result?.data as { job_id?: string } | undefined)?.job_id
      ElMessage.success({ message: '已进入队列', duration: 4000 })
      if (id) router.push({ name: 'jobs', query: { id } })
    } else if (done.status === 'failed') {
      ElMessage.error(done.result?.message ?? '执行失败')
    } else {
      ElMessage.warning(`${commandStatusText[done.status]}，agent 在线后会执行（10 分钟内有效）`)
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
    ElMessage.success(deletePolicy.value === 'purge' ? '已删除，备份文件删除命令已下发' : '已删除，备份保留至保留期结束')
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
  return p.incr_interval && goDuration(p.incr_interval) !== '0s' ? `${full} 全量 / ${goDuration(p.incr_interval)} 增量` : `${full} 全量`
}
</script>

<template>
  <div>
    <div class="page-header">
      <h2>项目</h2>
      <div>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
        <el-button type="primary" :icon="Plus" :disabled="!hosts.length" @click="create">新增项目</el-button>
      </div>
    </div>

    <div class="toolbar">
      <el-select v-model="hostFilter" clearable placeholder="全部主机" style="width: 200px">
        <el-option v-for="h in hosts" :key="h.id" :label="h.name" :value="h.id" />
      </el-select>
      <el-checkbox v-model="showDeleted" @change="load">显示已删除</el-checkbox>
    </div>

    <el-table v-loading="loading" :data="rows">
      <el-table-column label="项目" min-width="130">
        <template #default="{ row }">
          <b>{{ row.name }}</b>
          <el-tag v-if="row.tier === 'important'" size="small" type="danger" effect="plain" class="ml">重要</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="host_name" label="主机" min-width="120" />
      <el-table-column prop="container" label="容器" min-width="160" />
      <el-table-column label="排班" min-width="150">
        <template #default="{ row }">
          {{ schedule(row) }}
          <div class="muted">偏移 {{ offsetText(row.offset) }} · {{ row.incr_mode === 'differential' ? '差异' : '链式' }}</div>
        </template>
      </el-table-column>
      <el-table-column label="保留" width="70"><template #default="{ row }">{{ row.retention_days }} 天</template></el-table-column>
      <el-table-column label="配置" width="90">
        <template #default="{ row }">
          <el-tag v-if="row.deleted_at" type="info" size="small">已删除</el-tag>
          <el-tag v-else-if="row.applied" type="success" size="small">已生效</el-tag>
          <el-tag v-else type="warning" size="small">等待生效</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="70">
        <template #default="{ row }">
          <el-switch :model-value="!row.disabled" :disabled="!!row.deleted_at" size="small" @change="(v: string | number | boolean) => toggle(row, v as boolean)" />
        </template>
      </el-table-column>
      <el-table-column label="操作" width="260" fixed="right">
        <template #default="{ row }">
          <template v-if="!row.deleted_at">
            <el-dropdown trigger="click" class="run" @command="(t: JobType) => run(row, t)">
              <el-button link type="primary">立即执行</el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="full">全量</el-dropdown-item>
                  <el-dropdown-item command="incr" :disabled="!row.incr_interval || goDuration(row.incr_interval) === '0s'">增量</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
            <el-button link type="primary" @click="edit(row)">编辑</el-button>
            <el-button link type="primary" @click="router.push({ name: 'jobs', query: { project_id: row.id, tab: 'chains' } })">备份链</el-button>
            <el-button link type="danger" @click="askDelete(row)">删除</el-button>
          </template>
          <span v-else class="muted">{{ row.delete_policy === 'purge' ? '文件已/将删除' : '文件保留至过期' }}{{ row.purged_at ? '（已清理）' : '' }}</span>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialog" :title="editing ? `编辑项目 ${editing.name}` : '新增项目'" width="860px" destroy-on-close top="4vh">
      <ProjectForm v-if="templates" :hosts="hosts" :project="editing" :templates="templates" @saved="saved" @cancel="dialog = false" />
    </el-dialog>

    <el-dialog v-model="deleteDialog" :title="`删除项目 ${deleting?.name}`" width="520px">
      <p>删除后停止调度，项目从 agent 配置中移除。已有的备份文件：</p>
      <el-radio-group v-model="deletePolicy" class="policy">
        <el-radio value="keep">保留至保留期结束（{{ deleting?.retention_days }} 天）后自动删除</el-radio>
        <el-radio value="purge">立即删除全部备份文件</el-radio>
      </el-radio-group>
      <template v-if="deletePolicy === 'purge'">
        <el-alert type="error" show-icon :closable="false" title="备份文件删除后无法恢复" />
        <p>请输入项目名 <b>{{ deleting?.name }}</b> 确认：</p>
        <el-input v-model="confirmName" />
      </template>
      <template #footer>
        <el-button @click="deleting = null">取消</el-button>
        <el-button type="danger" :disabled="deletePolicy === 'purge' && confirmName !== deleting?.name" @click="doDelete">删除</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
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
