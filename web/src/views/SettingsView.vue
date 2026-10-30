<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { user } from '@/auth'
import { datetime } from '@/format'
import UsersPanel from './settings/UsersPanel.vue'
import AuditPanel from './settings/AuditPanel.vue'
import PageHeader from '@/components/PageHeader.vue'

const route = useRoute()
const router = useRouter()
const tab = ref(typeof route.query.tab === 'string' ? route.query.tab : 'users')
watch(tab, (t) => router.replace({ query: { tab: t } }))

const version = ref('')
onMounted(async () => {
  try {
    const r = await fetch('/healthz')
    version.value = (await r.json()).version
  } catch {
    version.value = '-'
  }
})
</script>

<template>
  <div class="bk-page">
    <PageHeader :title="$t('nav.settings')" :eyebrow="$t('settings.eyebrow')" :description="$t('settings.description')" />
    <el-tabs v-model="tab">
      <el-tab-pane :label="$t('settings.users')" name="users" lazy><UsersPanel /></el-tab-pane>
      <el-tab-pane :label="$t('settings.audit')" name="audit" lazy><AuditPanel /></el-tab-pane>
      <el-tab-pane :label="$t('settings.about')" name="about" lazy>
        <el-descriptions :column="1" border class="about">
          <el-descriptions-item :label="$t('users.me')">{{ user?.username }}</el-descriptions-item>
          <el-descriptions-item :label="$t('users.lastLogin')">{{ datetime(user?.last_login_at) }}</el-descriptions-item>
          <el-descriptions-item :label="$t('login.password')"><router-link :to="{ name: 'password' }">{{ $t('password.title') }}</router-link></el-descriptions-item>
          <el-descriptions-item :label="$t('settings.version')">{{ version }}</el-descriptions-item>
          <el-descriptions-item :label="$t('settings.wecom')">{{ $t('settings.wecomLater') }}</el-descriptions-item>
        </el-descriptions>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.about {
  max-width: 640px;
}
.about :deep(.el-descriptions__label) {
  width: 220px;
}
</style>
