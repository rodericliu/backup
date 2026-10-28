<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { api, errorMessage } from '@/api'
import { datetime } from '@/format'
import type { AuditLog, List } from '@/types'

// Audit log (PRD U-5).
const actionText: Record<string, string> = {
  login: '登录',
  login_failed: '登录失败',
  login_blocked: '登录被限流',
  logout: '登出',
  change_password: '修改密码',
  user_create: '新增用户',
  user_disable: '禁用用户',
  user_enable: '启用用户',
  user_reset_password: '重置密码',
  register_token_create: '生成注册 token',
  register_token_delete: '删除注册 token',
  host_register: '主机注册',
  host_revoke: '吊销主机',
  host_pause: '暂停调度',
  host_resume: '恢复调度',
  project_create: '新增项目',
  project_update: '修改项目',
  project_enable: '启用项目',
  project_disable: '停用项目',
  project_delete: '删除项目',
  project_purge: '删除备份文件',
  job_run: '立即执行',
  job_cancel: '取消任务',
}
const targetText: Record<string, string> = { user: '用户', host: '主机', project: '项目', job: '任务', register_token: '注册 token' }

const filter = reactive({ username: '', action: '', target_type: '', range: null as [Date, Date] | null })
const page = ref(1)
const pageSize = ref(50)
const logs = ref<AuditLog[]>([])
const total = ref(0)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const r = await api.get<List<AuditLog>>('/audit-logs', {
      username: filter.username.trim(),
      action: filter.action,
      target_type: filter.target_type,
      from: filter.range?.[0]?.toISOString(),
      to: filter.range?.[1]?.toISOString(),
      page: page.value,
      page_size: pageSize.value,
    })
    logs.value = r.items
    total.value = r.total
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(
  () => [filter.action, filter.target_type, filter.range],
  () => {
    page.value = 1
    load()
  },
)
watch([page, pageSize], load)

function detail(d: Record<string, unknown>) {
  return Object.entries(d ?? {})
    .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : v}`)
    .join('  ')
}
</script>

<template>
  <div>
    <div class="toolbar">
      <el-input v-model="filter.username" placeholder="用户名" clearable style="width: 160px" @change="load" />
      <el-select v-model="filter.action" clearable filterable placeholder="全部操作" style="width: 180px">
        <el-option v-for="(t, a) in actionText" :key="a" :label="t" :value="a" />
      </el-select>
      <el-select v-model="filter.target_type" clearable placeholder="全部对象" style="width: 140px">
        <el-option v-for="(t, k) in targetText" :key="k" :label="t" :value="k" />
      </el-select>
      <el-date-picker v-model="filter.range" type="datetimerange" range-separator="至" start-placeholder="开始" end-placeholder="结束" style="width: 360px" />
    </div>
    <el-table v-loading="loading" :data="logs" size="small">
      <el-table-column label="时间" width="170"><template #default="{ row }">{{ datetime(row.created_at) }}</template></el-table-column>
      <el-table-column label="用户" width="120">
        <template #default="{ row }">{{ row.username || (row.action === 'host_register' || row.action === 'project_purge' ? 'agent' : '-') }}</template>
      </el-table-column>
      <el-table-column label="操作" width="130">
        <template #default="{ row }">
          <el-tag :type="row.action.includes('fail') || row.action.includes('blocked') ? 'danger' : 'info'" size="small">{{ actionText[row.action] ?? row.action }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="对象" width="150">
        <template #default="{ row }">{{ targetText[row.target_type] ?? row.target_type }} <span class="mono">{{ row.target_id }}</span></template>
      </el-table-column>
      <el-table-column label="详情" min-width="300"><template #default="{ row }"><span class="mono">{{ detail(row.detail) }}</span></template></el-table-column>
      <el-table-column prop="ip" label="IP" width="130" />
    </el-table>
    <el-pagination
      v-model:current-page="page"
      v-model:page-size="pageSize"
      :total="total"
      :page-sizes="[20, 50, 100, 200]"
      layout="total, sizes, prev, pager, next"
      class="pager"
    />
  </div>
</template>

<style scoped>
.pager {
  margin-top: 12px;
  justify-content: flex-end;
}
</style>
