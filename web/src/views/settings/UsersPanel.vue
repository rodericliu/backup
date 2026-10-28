<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { user as me } from '@/auth'
import { ago, datetime } from '@/format'
import type { User } from '@/types'

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

const passwordRules = [
  { required: true, message: '请输入密码' },
  { min: 10, max: 72, message: '密码长度 10 ~ 72 个字符' },
]

const createDialog = ref(false)
const createForm = reactive({ username: '', password: '' })
const createRef = ref<FormInstance>()
const createRules: FormRules = {
  username: [
    { required: true, message: '请输入用户名' },
    { pattern: /^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/, message: '字母、数字或 _ . @ -，最长 64 个字符' },
  ],
  password: passwordRules,
}
function openCreate() {
  createForm.username = ''
  createForm.password = ''
  createDialog.value = true
}
async function create() {
  if (!(await createRef.value?.validate().catch(() => false))) return
  try {
    await api.post('/users', createForm)
    ElMessage.success('已创建，用户首次登录时需修改密码')
    createDialog.value = false
    load()
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

async function setDisabled(u: User, disabled: boolean) {
  if (disabled) {
    await ElMessageBox.confirm(`禁用 ${u.username}？该用户的会话会立即失效。`, '禁用用户', { type: 'warning', confirmButtonText: '禁用' })
  }
  try {
    Object.assign(u, await api.patch<User>(`/users/${u.id}`, { disabled }))
    ElMessage.success(disabled ? '已禁用' : '已启用')
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
    ElMessage.success('已重置，用户下次登录时需修改密码')
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
  <div>
    <div class="toolbar">
      <el-button type="primary" :icon="Plus" @click="openCreate">新增用户</el-button>
      <span class="muted">所有用户都是管理员。</span>
    </div>
    <el-table v-loading="loading" :data="users">
      <el-table-column label="用户名" min-width="140">
        <template #default="{ row }">
          <b>{{ row.username }}</b>
          <el-tag v-if="row.id === me?.id" size="small" class="ml">当前用户</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="160">
        <template #default="{ row }">
          <el-tag :type="row.disabled ? 'info' : 'success'" size="small">{{ row.disabled ? '已禁用' : '正常' }}</el-tag>
          <el-tag v-if="row.must_change_password" type="warning" size="small" class="ml">待修改密码</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近登录" min-width="160">
        <template #default="{ row }">
          <el-tooltip :content="datetime(row.last_login_at)" :disabled="!row.last_login_at"><span>{{ row.last_login_at ? ago(row.last_login_at) : '从未登录' }}</span></el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" min-width="160"><template #default="{ row }">{{ datetime(row.created_at) }}</template></el-table-column>
      <el-table-column label="操作" width="200">
        <template #default="{ row }">
          <el-button link type="primary" @click="openReset(row)">重置密码</el-button>
          <el-button v-if="!row.disabled" link type="danger" :disabled="row.id === me?.id" @click="setDisabled(row, true)">禁用</el-button>
          <el-button v-else link type="success" @click="setDisabled(row, false)">启用</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="createDialog" title="新增用户" width="460px">
      <el-form ref="createRef" :model="createForm" :rules="createRules" label-width="90px" @submit.prevent="create">
        <el-form-item label="用户名" prop="username"><el-input v-model="createForm.username" autocomplete="off" /></el-form-item>
        <el-form-item label="初始密码" prop="password">
          <el-input v-model="createForm.password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDialog = false">取消</el-button>
        <el-button type="primary" @click="create">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog :model-value="!!resetting" :title="`重置 ${resetting?.username} 的密码`" width="460px" @close="resetting = null">
      <el-form ref="resetRef" :model="resetForm" :rules="{ password: passwordRules }" label-width="90px" @submit.prevent="reset">
        <el-form-item label="临时密码" prop="password">
          <el-input v-model="resetForm.password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
      </el-form>
      <p class="muted">重置后该用户的所有会话失效，下次登录时必须修改密码。</p>
      <template #footer>
        <el-button @click="resetting = null">取消</el-button>
        <el-button type="primary" @click="reset">重置</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ml {
  margin-left: 6px;
}
</style>
