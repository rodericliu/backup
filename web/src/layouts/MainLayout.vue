<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowDown, Calendar, DataBoard, Files, Monitor, Setting, Tickets } from '@element-plus/icons-vue'
import { logout, user } from '@/auth'

const route = useRoute()
const router = useRouter()
const active = computed(() => '/' + (route.path.split('/')[1] ?? ''))

const menu = [
  { path: '/', title: '仪表盘', icon: DataBoard },
  { path: '/hosts', title: '主机', icon: Monitor },
  { path: '/projects', title: '项目', icon: Files },
  { path: '/schedule', title: '排班', icon: Calendar },
  { path: '/jobs', title: '任务历史', icon: Tickets },
  { path: '/settings', title: '设置', icon: Setting },
]

async function onCommand(cmd: string) {
  if (cmd === 'password') router.push({ name: 'password' })
  if (cmd === 'logout') {
    await logout()
    router.replace({ name: 'login' })
  }
}
</script>

<template>
  <el-container class="layout">
    <el-aside width="200px" class="aside">
      <div class="brand">MySQL 备份平台</div>
      <el-menu :default-active="active" router class="menu">
        <el-menu-item v-for="m in menu" :key="m.path" :index="m.path" :disabled="user?.must_change_password">
          <el-icon><component :is="m.icon" /></el-icon>
          <span>{{ m.title }}</span>
        </el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="header">
        <span class="title">{{ route.meta.title }}</span>
        <el-dropdown @command="onCommand">
          <span class="user">{{ user?.username }} <el-icon><arrow-down /></el-icon></span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="password">修改密码</el-dropdown-item>
              <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </el-header>
      <el-main>
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.layout {
  height: 100%;
}
.aside {
  background: #001529;
  color: #fff;
}
.brand {
  height: 56px;
  line-height: 56px;
  text-align: center;
  font-weight: 600;
  font-size: 16px;
  color: #fff;
}
.menu {
  border-right: none;
  --el-menu-bg-color: #001529;
  --el-menu-text-color: rgba(255, 255, 255, 0.75);
  --el-menu-hover-bg-color: #0c2a47;
  --el-menu-active-color: #fff;
}
.menu :deep(.el-menu-item.is-active) {
  background: var(--el-color-primary);
}
.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #fff;
  border-bottom: 1px solid var(--el-border-color-light);
}
.title {
  font-size: 16px;
  font-weight: 600;
}
.user {
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
}
</style>
