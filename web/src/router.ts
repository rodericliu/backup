import { createRouter, createWebHistory } from 'vue-router'
import { loadUser, signedOut, user } from './auth'
import { setUnauthorizedHandler } from './api'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('./layouts/MainLayout.vue'),
      children: [
        { path: '', name: 'dashboard', component: () => import('./views/DashboardView.vue'), meta: { title: '仪表盘' } },
        { path: 'hosts', name: 'hosts', component: () => import('./views/HostsView.vue'), meta: { title: '主机' } },
        { path: 'projects', name: 'projects', component: () => import('./views/ProjectsView.vue'), meta: { title: '项目' } },
        { path: 'schedule', name: 'schedule', component: () => import('./views/ScheduleView.vue'), meta: { title: '排班' } },
        { path: 'jobs', name: 'jobs', component: () => import('./views/JobsView.vue'), meta: { title: '任务历史' } },
        { path: 'settings', name: 'settings', component: () => import('./views/SettingsView.vue'), meta: { title: '设置' } },
        {
          path: 'password',
          name: 'password',
          component: () => import('./views/PasswordView.vue'),
          meta: { title: '修改密码', allowMustChange: true },
        },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  if (to.meta.public) return true
  const u = await loadUser()
  if (!u) return { name: 'login', query: { redirect: to.fullPath } }
  // The initial password must be changed first (PRD U-2).
  if (u.must_change_password && !to.meta.allowMustChange) return { name: 'password' }
  return true
})

router.afterEach((to) => {
  const title = to.meta.title as string | undefined
  document.title = title ? `${title} - MySQL 从库备份平台` : 'MySQL 从库备份平台'
})

setUnauthorizedHandler(() => {
  if (!user.value) return
  signedOut()
  const current = router.currentRoute.value
  if (!current.meta.public) router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

export default router
