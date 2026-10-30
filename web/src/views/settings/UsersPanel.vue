<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { user as me } from '@/auth'
import { ago, datetime } from '@/format'
import type { User } from '@/types'
import { t } from '@/i18n'

// User management (PRD U-4). All users are administrators.
const users = ref<User[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    users.value = await api.get<User[]>('/users')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)

const passwordRules = computed(() => [
  { required: true, message: t('login.passwordRequired') },
  { min: 10, max: 72, message: t('password.length') },
])

const createDialog = ref(false)
const createForm = reactive({ username: '', password: '' })
const createRef = ref<FormInstance>()
const createRules = computed<FormRules>(() => ({
  username: [
    { required: true, message: t('login.usernameRequired') },
    { pattern: /^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/, message: t('users.usernamePattern') },
  ],
  password: passwordRules.value,
}))
function openCreate() {
  createForm.username = ''
  createForm.password = ''
  createDialog.value = true
}
async function create() {
  if (!(await createRef.value?.validate().catch(() => false))) return
  try {
    await api.post('/users', createForm)
    ElMessage.success(t('users.created'))
    createDialog.value = false
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

async function setDisabled(u: User, disabled: boolean) {
  if (disabled) {
    await ElMessageBox.confirm(t('users.disableConfirm', { name: u.username }), t('users.disableTitle'), {
      type: 'warning',
      confirmButtonText: t('users.disable'),
    })
  }
  try {
    Object.assign(u, await api.patch<User>(`/users/${u.id}`, { disabled }))
    ElMessage.success(disabled ? t('users.disabledMsg') : t('projects.enabled'))
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

const resetting = ref<User | null>(null)
const resetForm = reactive({ password: '' })
const resetRef = ref<FormInstance>()
async function reset() {
  if (!(await resetRef.value?.validate().catch(() => false))) return
  try {
    await api.post(`/users/${resetting.value!.id}/password`, resetForm)
    ElMessage.success(t('users.resetDone'))
    resetting.value = null
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}
function openReset(u: User) {
  resetForm.password = ''
  resetting.value = u
}
</script>

<template>
  <div class="bk-panel">
    <div class="toolbar">
      <el-button type="primary" :icon="Plus" @click="openCreate">{{ $t('users.add') }}</el-button>
      <span class="muted">{{ $t('users.allAdmins') }}</span>
    </div>
    <el-table v-loading="loading" :data="users">
      <el-table-column :label="$t('login.username')" min-width="140">
        <template #default="{ row }">
          <b>{{ row.username }}</b>
          <el-tag v-if="row.id === me?.id" size="small" class="ml">{{ $t('users.me') }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="$t('job.status')" width="200">
        <template #default="{ row }">
          <el-tag :type="row.disabled ? 'info' : 'success'" size="small">{{ row.disabled ? $t('users.disabledMsg') : $t('dashboard.healthy') }}</el-tag>
          <el-tag v-if="row.must_change_password" type="warning" size="small" class="ml">{{ $t('users.mustChange') }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="$t('users.lastLogin')" min-width="160">
        <template #default="{ row }">
          <el-tooltip :content="datetime(row.last_login_at)" :disabled="!row.last_login_at"><span>{{ row.last_login_at ? ago(row.last_login_at) : $t('users.never') }}</span></el-tooltip>
        </template>
      </el-table-column>
      <el-table-column :label="$t('hosts.createdAt')" min-width="160"><template #default="{ row }">{{ datetime(row.created_at) }}</template></el-table-column>
      <el-table-column :label="$t('projects.actions')" width="220">
        <template #default="{ row }">
          <el-button link type="primary" @click="openReset(row)">{{ $t('users.resetPassword') }}</el-button>
          <el-button v-if="!row.disabled" link type="danger" :disabled="row.id === me?.id" @click="setDisabled(row, true)">{{ $t('users.disable') }}</el-button>
          <el-button v-else link type="success" @click="setDisabled(row, false)">{{ $t('users.enable') }}</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="createDialog" :title="$t('users.add')" width="480px">
      <el-form ref="createRef" :model="createForm" :rules="createRules" :label-width="$t('users.labelWidth')" @submit.prevent="create">
        <el-form-item :label="$t('login.username')" prop="username"><el-input v-model="createForm.username" autocomplete="off" /></el-form-item>
        <el-form-item :label="$t('users.initialPassword')" prop="password">
          <el-input v-model="createForm.password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDialog = false">{{ $t('common.cancel') }}</el-button>
        <el-button type="primary" @click="create">{{ $t('common.create') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog :model-value="!!resetting" :title="$t('users.resetTitle', { name: resetting?.username })" width="480px" @close="resetting = null">
      <el-form ref="resetRef" :model="resetForm" :rules="{ password: passwordRules }" :label-width="$t('users.labelWidth')" @submit.prevent="reset">
        <el-form-item :label="$t('users.tempPassword')" prop="password">
          <el-input v-model="resetForm.password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
      </el-form>
      <p class="muted">{{ $t('users.resetHint') }}</p>
      <template #footer>
        <el-button @click="resetting = null">{{ $t('common.cancel') }}</el-button>
        <el-button type="primary" @click="reset">{{ $t('users.reset') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ml {
  margin-left: 6px;
}
</style>
