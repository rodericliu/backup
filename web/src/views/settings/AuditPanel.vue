<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { api, errorMessage } from '@/api'
import { datetime } from '@/format'
import type { AuditLog, List } from '@/types'
import { keyed } from '@/i18n'

// Audit log (PRD U-5).
const actions = [
  'login',
  'login_failed',
  'login_blocked',
  'logout',
  'change_password',
  'user_create',
  'user_disable',
  'user_enable',
  'user_reset_password',
  'register_token_create',
  'register_token_delete',
  'host_register',
  'host_revoke',
  'host_pause',
  'host_resume',
  'project_create',
  'project_update',
  'project_enable',
  'project_disable',
  'project_delete',
  'project_purge',
  'job_run',
  'job_cancel',
]
const actionText = keyed('audit.actions')
const targets = ['user', 'host', 'project', 'job', 'register_token']
const targetText = keyed('audit.targets')

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
  <div class="bk-panel">
    <div class="toolbar">
      <el-input v-model="filter.username" :placeholder="$t('login.username')" clearable style="width: 160px" @change="load" />
      <el-select v-model="filter.action" clearable filterable :placeholder="$t('audit.allActions')" style="width: 200px">
        <el-option v-for="a in actions" :key="a" :label="actionText[a]" :value="a" />
      </el-select>
      <el-select v-model="filter.target_type" clearable :placeholder="$t('audit.allTargets')" style="width: 160px">
        <el-option v-for="k in targets" :key="k" :label="targetText[k]" :value="k" />
      </el-select>
      <el-date-picker
        v-model="filter.range"
        type="datetimerange"
        :range-separator="$t('jobs.rangeTo')"
        :start-placeholder="$t('audit.start')"
        :end-placeholder="$t('audit.end')"
        style="width: 360px"
      />
    </div>
    <el-table v-loading="loading" :data="logs" size="small">
      <el-table-column :label="$t('dashboard.time')" width="185"><template #default="{ row }"><span class="bk-num">{{ datetime(row.created_at) }}</span></template></el-table-column>
      <el-table-column :label="$t('audit.user')" width="120">
        <template #default="{ row }">{{ row.username || (row.action === 'host_register' || row.action === 'project_purge' ? 'agent' : '-') }}</template>
      </el-table-column>
      <el-table-column :label="$t('audit.action')" width="170">
        <template #default="{ row }">
          <el-tag :type="row.action.includes('fail') || row.action.includes('blocked') ? 'danger' : 'info'" size="small">{{ actionText[row.action] ?? row.action }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="$t('audit.target')" width="170">
        <template #default="{ row }">{{ targetText[row.target_type] ?? row.target_type }} <span class="mono">{{ row.target_id }}</span></template>
      </el-table-column>
      <el-table-column :label="$t('audit.detail')" min-width="300"><template #default="{ row }"><span class="mono">{{ detail(row.detail) }}</span></template></el-table-column>
      <el-table-column label="IP" width="130"><template #default="{ row }"><span class="bk-data">{{ row.ip }}</span></template></el-table-column>
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
  justify-content: flex-end;
  padding: 12px var(--bk-panel-padding);
  border-top: 1px solid var(--bk-border);
}
</style>
