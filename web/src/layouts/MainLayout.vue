<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowDown, Calendar, ChatDotRound, Close, DataBoard, Expand, Files, Key, Monitor, Reading, Setting, SwitchButton, Tickets } from '@element-plus/icons-vue'
import { logout, user } from '@/auth'
import AppLogo from '@/components/AppLogo.vue'
import ThemeSwitcher from '@/components/ThemeSwitcher.vue'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'
import AssistantDrawer from '@/components/assistant/AssistantDrawer.vue'
import { assistantOpen, assistantWidth, enabled as assistantEnabled, loadInfo } from '@/composables/assistant'

const route = useRoute()
const router = useRouter()
const active = computed(() => '/' + (route.path.split('/')[1] ?? ''))

const sections = [
  {
    heading: 'nav.monitor',
    items: [
      { path: '/', title: 'nav.dashboard', icon: DataBoard },
      { path: '/hosts', title: 'nav.hosts', icon: Monitor },
    ],
  },
  {
    heading: 'nav.backup',
    items: [
      { path: '/projects', title: 'nav.projects', icon: Files },
      { path: '/schedule', title: 'nav.schedule', icon: Calendar },
      { path: '/jobs', title: 'nav.jobs', icon: Tickets },
    ],
  },
  {
    heading: 'nav.manual',
    items: [{ path: '/manual/xtrabackup24', title: 'nav.xtrabackup', icon: Reading }],
  },
]
const isActive = (path: string) => (path === '/' ? route.path === '/' : route.path === path || route.path.startsWith(path + '/'))

const locked = computed(() => !!user.value?.must_change_password)

// Overlay navigation on narrow screens.
const navOpen = ref(false)
watch(
  () => route.fullPath,
  () => (navOpen.value = false),
)

onMounted(loadInfo)
const showAssistant = computed(() => assistantEnabled.value && assistantOpen.value && !locked.value)

const initials = computed(() => (user.value?.username ?? '?').slice(0, 2).toUpperCase())

async function onCommand(cmd: string) {
  if (cmd === 'password') router.push({ name: 'password' })
  if (cmd === 'settings') router.push({ name: 'settings' })
  if (cmd === 'logout') {
    await logout()
    router.replace({ name: 'login' })
  }
}
</script>

<template>
  <div class="layout" :class="{ 'nav-open': navOpen, 'assistant-open': showAssistant }" :style="{ '--bk-assistant-width': assistantWidth + 'px' }">
    <header class="navbar">
      <button type="button" class="icon-btn nav-toggle" :aria-label="$t('nav.open')" :aria-expanded="navOpen" aria-controls="app-navigation" @click="navOpen = true">
        <el-icon :size="20"><Expand /></el-icon>
      </button>
      <router-link to="/" class="brand" :aria-label="`${$t('app.name')} · ${$t('nav.dashboard')}`">
        <AppLogo />
        <span class="wordmark">Backup</span>
      </router-link>
      <span v-if="route.meta.title" class="navbar-location">{{ $t(route.meta.title as string) }}</span>
      <span class="spacer" />
      <div class="navbar-actions">
        <button
          v-if="assistantEnabled && !locked"
          type="button"
          class="assistant-toggle"
          :class="{ active: assistantOpen }"
          :aria-pressed="assistantOpen"
          aria-controls="assistant-drawer"
          :title="$t('nav.assistant')"
          @click="assistantOpen = !assistantOpen"
        >
          <el-icon :size="18"><ChatDotRound /></el-icon>
          <span class="assistant-label">{{ $t('nav.assistantShort') }}</span>
        </button>
        <LocaleSwitcher />
        <ThemeSwitcher />
        <el-dropdown trigger="click" placement="bottom-end" @command="onCommand">
          <button type="button" class="profile" :aria-label="$t('nav.accountMenu', { name: user?.username })">
            <span class="avatar">{{ initials }}</span>
            <span class="profile-name">{{ user?.username }}</span>
            <el-icon :size="14"><ArrowDown /></el-icon>
          </button>
          <template #dropdown>
            <div class="profile-head">
              <div class="profile-head__name">{{ user?.username }}</div>
              <div class="profile-head__sub">{{ $t('nav.admin') }}</div>
            </div>
            <el-dropdown-menu>
              <el-dropdown-item command="settings" :icon="Setting" :disabled="locked">{{ $t('nav.settings') }}</el-dropdown-item>
              <el-dropdown-item command="password" :icon="Key">{{ $t('password.title') }}</el-dropdown-item>
              <el-dropdown-item command="logout" :icon="SwitchButton" divided class="logout">{{ $t('nav.logout') }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </header>

    <aside id="app-navigation" class="sidenav" :aria-label="$t('nav.main')">
      <div class="sidenav-head">
        <span class="brand">
          <AppLogo />
          <span class="wordmark">Backup</span>
        </span>
        <button type="button" class="icon-btn" :aria-label="$t('nav.close')" @click="navOpen = false">
          <el-icon :size="20"><Close /></el-icon>
        </button>
      </div>
      <nav class="nav-items">
        <template v-for="s in sections" :key="s.heading">
          <div class="nav-section-title">{{ $t(s.heading) }}</div>
          <component
            :is="locked ? 'span' : 'router-link'"
            v-for="m in s.items"
            :key="m.path"
            :to="m.path"
            class="nav-link"
            :class="{ active: isActive(m.path), disabled: locked }"
            :aria-current="isActive(m.path) ? 'page' : undefined"
          >
            <el-icon :size="20"><component :is="m.icon" /></el-icon>
            <span>{{ $t(m.title) }}</span>
          </component>
        </template>
      </nav>
      <component :is="locked ? 'span' : 'router-link'" to="/settings" class="nav-account" :class="{ active: active === '/settings', disabled: locked }">
        <el-icon :size="20"><Setting /></el-icon>
        <span>{{ $t('nav.settings') }}</span>
      </component>
    </aside>
    <div class="scrim" @click="navOpen = false" />

    <main class="content">
      <div class="content-inner">
        <router-view />
      </div>
    </main>
    <AssistantDrawer v-if="showAssistant" />
  </div>
</template>

<style scoped>
.layout {
  min-block-size: 100%;
}

/* ---------- Navbar ---------- */
.navbar {
  position: fixed;
  z-index: 13;
  inset-block-start: 0;
  inset-inline: 0;
  display: flex;
  align-items: center;
  gap: 12px;
  block-size: var(--bk-navbar-height);
  padding-inline: 24px 16px;
  background: var(--bk-surface);
  box-shadow: 0 1px 0 0 var(--bk-border);
}
.brand {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  gap: 10px;
  color: var(--bk-on-surface);
  text-decoration: none;
}
.brand:hover {
  text-decoration: none;
}
.wordmark {
  font-size: 1.0625rem;
  font-weight: 700;
  letter-spacing: 0.06em;
}
.navbar-location {
  margin-inline-start: 12px;
  padding-inline-start: 24px;
  border-inline-start: 1px solid var(--bk-border);
  color: var(--bk-secondary);
  font-size: 0.8125rem;
}
.spacer {
  flex: 1;
}
.navbar-actions {
  display: flex;
  align-items: center;
  gap: 4px;
}
.icon-btn {
  display: inline-grid;
  place-items: center;
  inline-size: 40px;
  block-size: 40px;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--bk-secondary);
  cursor: pointer;
}
.icon-btn:hover {
  background: var(--bk-hover);
  color: var(--bk-on-surface);
}
.nav-toggle {
  display: none;
  margin-inline-start: -8px;
}
.assistant-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  block-size: 40px;
  padding-inline: 12px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--bk-secondary);
  font: inherit;
  font-size: 0.875rem;
  cursor: pointer;
}
.assistant-toggle:hover {
  background: var(--bk-hover);
  color: var(--bk-on-surface);
}
.assistant-toggle.active {
  background: rgba(var(--bk-primary-rgb), 0.09);
  color: var(--el-color-primary);
}
.profile {
  display: flex;
  align-items: center;
  gap: 8px;
  min-block-size: 44px;
  padding-inline: 8px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--bk-on-surface);
  font: inherit;
  font-size: 0.875rem;
  cursor: pointer;
}
.profile:hover {
  background: var(--bk-hover);
}
.avatar {
  display: inline-grid;
  place-items: center;
  inline-size: 26px;
  block-size: 26px;
  border-radius: 50%;
  background: rgba(var(--bk-primary-rgb), 0.14);
  color: var(--el-color-primary);
  font-size: 0.6875rem;
  font-weight: 600;
}
.profile-name {
  max-inline-size: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.profile-head {
  min-inline-size: 200px;
  padding: 10px 16px 8px;
  border-block-end: 1px solid var(--bk-border);
}
.profile-head__name {
  font-weight: 600;
}
.profile-head__sub {
  color: var(--bk-secondary);
  font-size: 12px;
}
:deep(.logout) {
  color: var(--el-color-danger);
}

/* ---------- Vertical nav ---------- */
.sidenav {
  position: fixed;
  z-index: 12;
  inset-block: var(--bk-navbar-height) 0;
  inset-inline-start: 0;
  display: flex;
  flex-direction: column;
  inline-size: var(--bk-nav-width);
  background: var(--bk-surface);
  border-inline-end: 1px solid var(--bk-border);
}
.sidenav-head {
  display: none;
  align-items: center;
  justify-content: space-between;
  block-size: var(--bk-navbar-height);
  padding-inline: 24px 12px;
  flex-shrink: 0;
}
.nav-items {
  flex: 1;
  min-block-size: 0;
  overflow-y: auto;
  padding-block: 4px 16px;
}
.nav-section-title {
  margin: 20px 24px 10px;
  color: var(--bk-secondary);
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.08em;
}
.nav-link,
.nav-account {
  box-sizing: border-box;
}
.nav-link {
  display: flex;
  align-items: center;
  gap: 12px;
  min-block-size: 44px;
  margin: 4px 12px;
  padding: 10px 12px;
  border-radius: 8px;
  color: var(--bk-secondary);
  font-size: 0.875rem;
  text-decoration: none;
  transition: background-color var(--bk-motion), color var(--bk-motion);
}
.nav-link:hover {
  background: var(--bk-hover);
  color: var(--bk-on-surface);
  text-decoration: none;
}
.nav-link.active {
  background: rgba(var(--bk-primary-rgb), 0.09);
  box-shadow: inset 3px 0 0 var(--el-color-primary);
  color: var(--el-color-primary);
  font-weight: 600;
}
.nav-link.disabled,
.nav-account.disabled {
  opacity: 0.45;
  pointer-events: none;
}
.nav-account {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  min-block-size: 60px;
  padding: 16px 24px;
  border-block-start: 1px solid var(--bk-border);
  color: var(--bk-secondary);
  font-size: 0.8125rem;
  text-decoration: none;
}
.nav-account:hover,
.nav-account.active {
  background: rgba(var(--bk-primary-rgb), 0.06);
  color: var(--el-color-primary);
  text-decoration: none;
}
.scrim {
  display: none;
}

/* ---------- Content ---------- */
.content {
  min-block-size: 100dvh;
  box-sizing: border-box;
  padding-block-start: var(--bk-navbar-height);
  padding-inline-start: var(--bk-nav-width);
}
.content-inner {
  box-sizing: border-box;
  max-inline-size: var(--bk-content-max);
  margin-inline: auto;
  padding: 28px 24px 48px;
}

@media (min-width: 1280px) {
  .assistant-open .content {
    padding-inline-end: var(--bk-assistant-width);
  }
}

@media (max-width: 1279px) {
  .nav-toggle {
    display: inline-grid;
  }
  .navbar-location {
    display: none;
  }
  .sidenav {
    z-index: 14;
    inset-block-start: 0;
    transform: translateX(-100%);
    transition: transform 0.25s ease-in-out;
  }
  .sidenav-head {
    display: flex;
  }
  .nav-open .sidenav {
    transform: none;
    box-shadow: var(--bk-shadow);
  }
  .scrim {
    position: fixed;
    z-index: 13;
    inset: 0;
    display: block;
    background: var(--el-overlay-color-lighter);
    opacity: 0;
    pointer-events: none;
    transition: opacity 0.25s ease-in-out;
  }
  .nav-open .scrim {
    opacity: 1;
    pointer-events: auto;
  }
  .content {
    padding-inline-start: 0;
  }
}

@media (max-width: 599px) {
  .navbar {
    gap: 4px;
    padding-inline: 16px 8px;
  }
  .profile-name,
  .assistant-label {
    display: none;
  }
  .content-inner {
    padding: 20px 16px 40px;
  }
}
</style>
