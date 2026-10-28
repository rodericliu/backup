<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { FormInstance, FormRules } from 'element-plus'
import { errorMessage } from '@/api'
import { login, user } from '@/auth'

const route = useRoute()
const router = useRouter()
const form = reactive({ username: '', password: '' })
const formRef = ref<FormInstance>()
const loading = ref(false)
const error = ref('')
const rules: FormRules = {
  username: [{ required: true, message: '请输入用户名' }],
  password: [{ required: true, message: '请输入密码' }],
}

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  loading.value = true
  error.value = ''
  try {
    await login(form.username, form.password)
    if (user.value?.must_change_password) {
      router.replace({ name: 'password' })
    } else {
      const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : '/'
      router.replace(redirect)
    }
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login">
    <el-card class="box" shadow="always">
      <h2>MySQL 从库备份平台</h2>
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名" prop="username">
          <el-input v-model="form.username" autocomplete="username" autofocus />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input v-model="form.password" type="password" autocomplete="current-password" show-password />
        </el-form-item>
        <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" class="error" />
        <el-button type="primary" native-type="submit" :loading="loading" class="submit">登录</el-button>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.login {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #001529, #1d4f80);
}
.box {
  width: 360px;
}
h2 {
  text-align: center;
  margin: 0 0 20px;
}
.error {
  margin-bottom: 16px;
}
.submit {
  width: 100%;
}
</style>
