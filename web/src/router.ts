import { computed, watch } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import { loadUser, signedOut, user } from './auth'
import { setUnauthorizedHandler } from './api'
import { t } from './i18n'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('./layouts/MainLayout.vue'),
      children: [
        { path: '', name: 'dashboard', component: () => import('./views/DashboardView.vue'), meta: { title: 'nav.dashboard' } },
        { path: 'hosts', name: 'hosts', component: () => import('./views/HostsView.vue'), meta: { title: 'nav.hosts' } },
        { path: 'projects', name: 'projects', component: () => import('./views/ProjectsView.vue'), meta: { title: 'nav.projects' } },
        { path: 'schedule', name: 'schedule', component: () => import('./views/ScheduleView.vue'), meta: { title: 'nav.schedule' } },
        { path: 'jobs', name: 'jobs', component: () => import('./views/JobsView.vue'), meta: { title: 'nav.jobs' } },
        {
          path: 'manual/xtrabackup24',
          name: 'manual-xtrabackup24',
          component: () => import('./views/manual/Xtrabackup24View.vue'),
          meta: { title: 'nav.manualXtrabackup' },
        },
        { path: 'settings', name: 'settings', component: () => import('./views/SettingsView.vue'), meta: { title: 'nav.settings' } },
        {
          path: 'password',
          name: 'password',
          component: () => import('./views/PasswordView.vue'),
          meta: { title: 'password.title', allowMustChange: true },
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

// meta.title is a message key; the document title follows the language.
const currentTitle = computed(() => {
  const key = router.currentRoute.value.meta.title as string | undefined
  return key ? `${t(key)} - ${t('app.name')}` : t('app.name')
})
watch(currentTitle, (title) => (document.title = title), { immediate: true })

setUnauthorizedHandler(() => {
  if (!user.value) return
  signedOut()
  const current = router.currentRoute.value
  if (!current.meta.public) router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

export default router
