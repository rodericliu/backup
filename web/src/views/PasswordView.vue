<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { api, errorMessage } from '@/api'
import { loadUser, user } from '@/auth'

const router = useRouter()
const form = reactive({ old: '', password: '', confirm: '' })
const formRef = ref<FormInstance>()
const loading = ref(false)
const rules: FormRules = {
  old: [{ required: true, message: '请输入当前密码' }],
  password: [
    { required: true, message: '请输入新密码' },
    { min: 10, max: 72, message: '密码长度 10 ~ 72 个字符' },
  ],
  confirm: [
    {
      validator: (_r, v, cb) => (v === form.password ? cb() : cb(new Error('两次输入的密码不一致'))),
    },
  ],
}

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  loading.value = true
  try {
    await api.post('/auth/password', { old_password: form.old, new_password: form.password })
    await loadUser(true)
    ElMessage.success('密码已修改，其他会话已退出')
    router.replace('/')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <el-card class="box">
    <el-alert
      v-if="user?.must_change_password"
      title="首次登录或密码已被重置，请先修改密码"
      type="warning"
      show-icon
      :closable="false"
      class="tip"
    />
    <el-form ref="formRef" :model="form" :rules="rules" label-width="100px" @submit.prevent="submit">
      <el-form-item label="当前密码" prop="old">
        <el-input v-model="form.old" type="password" autocomplete="current-password" show-password />
      </el-form-item>
      <el-form-item label="新密码" prop="password">
        <el-input v-model="form.password" type="password" autocomplete="new-password" show-password />
      </el-form-item>
      <el-form-item label="确认新密码" prop="confirm">
        <el-input v-model="form.confirm" type="password" autocomplete="new-password" show-password />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" native-type="submit" :loading="loading">修改密码</el-button>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<style scoped>
.box {
  max-width: 520px;
}
.tip {
  margin-bottom: 16px;
}
</style>
