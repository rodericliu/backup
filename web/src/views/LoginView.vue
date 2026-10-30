<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { FormInstance, FormRules } from 'element-plus'
import { Key } from '@element-plus/icons-vue'
import AppLogo from '@/components/AppLogo.vue'
import ThemeSwitcher from '@/components/ThemeSwitcher.vue'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'
import { t } from '@/i18n'
import { errorMessage } from '@/api'
import { login, user } from '@/auth'

const route = useRoute()
const router = useRouter()
const form = reactive({ username: '', password: '' })
const formRef = ref<FormInstance>()
const loading = ref(false)
const error = ref('')
const rules = computed<FormRules>(() => ({
  username: [{ required: true, message: t('login.usernameRequired') }],
  password: [{ required: true, message: t('login.passwordRequired') }],
}))

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
  <main class="auth-shell">
    <div class="auth-shell__controls"><LocaleSwitcher /><ThemeSwitcher /></div>
    <section class="auth-card">
      <span class="auth-brand">
        <AppLogo :size="32" />
        <span class="wordmark">Backup</span>
      </span>
      <p class="auth-eyebrow">{{ $t('app.name') }}</p>
      <h1>{{ $t('login.title') }}</h1>
      <p class="auth-description">{{ $t('login.description') }}</p>
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" size="large" class="auth-form" @submit.prevent="submit">
        <el-form-item :label="$t('login.username')" prop="username">
          <el-input v-model="form.username" autocomplete="username" autocapitalize="none" :spellcheck="false" autofocus />
        </el-form-item>
        <el-form-item :label="$t('login.password')" prop="password">
          <el-input v-model="form.password" type="password" autocomplete="current-password" show-password />
        </el-form-item>
        <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" class="error" />
        <el-button type="primary" native-type="submit" :loading="loading" class="submit">{{ loading ? $t('login.submitting') : $t('login.submit') }}</el-button>
      </el-form>
      <div class="auth-footer">
        <el-icon :size="16"><Key /></el-icon>
        {{ $t('login.footer') }}
      </div>
    </section>
  </main>
</template>

<style scoped>
.auth-shell {
  position: relative;
  display: grid;
  place-items: center;
  box-sizing: border-box;
  min-block-size: 100dvh;
  padding: 88px 24px 48px;
  background:
    radial-gradient(circle at 15% 20%, rgba(var(--bk-primary-rgb), 0.1), transparent 40%),
    radial-gradient(circle at 85% 80%, rgba(var(--bk-primary-rgb), 0.08), transparent 45%),
    var(--bk-background);
}
.auth-shell__controls {
  position: absolute;
  z-index: 1;
  inset-block-start: max(16px, env(safe-area-inset-top));
  inset-inline-end: 20px;
  display: flex;
  gap: 4px;
}
.auth-card {
  position: relative;
  box-sizing: border-box;
  inline-size: min(448px, 100%);
  padding: 32px;
  border: 1px solid var(--bk-border);
  border-radius: 16px;
  background: var(--bk-surface);
  box-shadow: var(--bk-shadow);
}
.auth-brand {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  color: var(--bk-on-surface);
}
.wordmark {
  font-size: 1.25rem;
  font-weight: 700;
  letter-spacing: 0.06em;
}
.auth-eyebrow {
  margin: 32px 0 8px;
  color: var(--bk-secondary);
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.07em;
}
h1 {
  margin: 0;
  font-size: 1.75rem;
  font-weight: 650;
  letter-spacing: -0.03em;
  line-height: 1.3;
}
.auth-description {
  margin: 12px 0 0;
  color: var(--bk-secondary);
  font-size: 0.875rem;
  line-height: 1.6;
}
.auth-form {
  margin-block-start: 28px;
}
.error {
  margin-bottom: 16px;
}
.submit {
  width: 100%;
  min-height: 48px;
}
.auth-footer {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin-block-start: 24px;
  padding-block-start: 20px;
  border-block-start: 1px solid var(--bk-border);
  color: var(--bk-secondary);
  font-size: 0.75rem;
}
@media (max-width: 599px) {
  .auth-shell {
    padding-inline: 16px;
  }
  .auth-card {
    padding: 24px;
  }
}
</style>
