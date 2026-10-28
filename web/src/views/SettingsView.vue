<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { user } from '@/auth'
import { datetime } from '@/format'
import UsersPanel from './settings/UsersPanel.vue'
import AuditPanel from './settings/AuditPanel.vue'

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
  <div>
    <div class="page-header"><h2>设置</h2></div>
    <el-tabs v-model="tab">
      <el-tab-pane label="用户管理" name="users" lazy><UsersPanel /></el-tab-pane>
      <el-tab-pane label="审计日志" name="audit" lazy><AuditPanel /></el-tab-pane>
      <el-tab-pane label="账号与系统" name="about" lazy>
        <el-descriptions :column="1" border style="max-width: 640px">
          <el-descriptions-item label="当前用户">{{ user?.username }}</el-descriptions-item>
          <el-descriptions-item label="最近登录">{{ datetime(user?.last_login_at) }}</el-descriptions-item>
          <el-descriptions-item label="密码"><router-link :to="{ name: 'password' }">修改密码</router-link></el-descriptions-item>
          <el-descriptions-item label="backup-server 版本">{{ version }}</el-descriptions-item>
          <el-descriptions-item label="企业微信日报与告警">第二阶段（P5）提供</el-descriptions-item>
        </el-descriptions>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>
