<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { api, errorMessage } from '@/api'
import { loadUser, user } from '@/auth'
import PageHeader from '@/components/PageHeader.vue'
import { t } from '@/i18n'

const router = useRouter()
const form = reactive({ old: '', password: '', confirm: '' })
const formRef = ref<FormInstance>()
const loading = ref(false)
const rules = computed<FormRules>(() => ({
  old: [{ required: true, message: t('password.oldRequired') }],
  password: [
    { required: true, message: t('password.newRequired') },
    { min: 10, max: 72, message: t('password.length') },
  ],
  confirm: [
    {
      validator: (_r, v, cb) => (v === form.password ? cb() : cb(new Error(t('password.mismatch')))),
    },
  ],
}))

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  loading.value = true
  try {
    await api.post('/auth/password', { old_password: form.old, new_password: form.password })
    await loadUser(true)
    ElMessage.success(t('password.changed'))
    router.replace('/')
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="bk-page">
  <PageHeader :title="$t('password.title')" :eyebrow="$t('password.eyebrow')" :description="$t('password.description')" />
  <div class="bk-panel box">
  <div class="bk-panel__body">
    <el-alert
      v-if="user?.must_change_password"
      :title="$t('password.mustChange')"
      type="warning"
      show-icon
      :closable="false"
      class="tip"
    />
    <el-form ref="formRef" :model="form" :rules="rules" :label-width="$t('password.labelWidth')" @submit.prevent="submit">
      <el-form-item :label="$t('password.old')" prop="old">
        <el-input v-model="form.old" type="password" autocomplete="current-password" show-password />
      </el-form-item>
      <el-form-item :label="$t('password.new')" prop="password">
        <el-input v-model="form.password" type="password" autocomplete="new-password" show-password />
      </el-form-item>
      <el-form-item :label="$t('password.confirm')" prop="confirm">
        <el-input v-model="form.confirm" type="password" autocomplete="new-password" show-password />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" native-type="submit" :loading="loading">{{ $t('password.title') }}</el-button>
      </el-form-item>
    </el-form>
  </div>
  </div>
  </div>
</template>

<style scoped>
.box {
  max-width: 560px;
}
.tip {
  margin-bottom: 16px;
}
</style>
