<script setup lang="ts">
import { computed } from 'vue'
import { Monitor, Moon, Sunny } from '@element-plus/icons-vue'
import { setTheme, themePreference, type ThemePreference } from '@/theme'

const options = [
  { value: 'light', icon: Sunny },
  { value: 'dark', icon: Moon },
  { value: 'system', icon: Monitor },
] as const

const current = computed(() => options.find((o) => o.value === themePreference.value) ?? options[2])
</script>

<template>
  <el-dropdown trigger="click" placement="bottom-end" @command="(t: ThemePreference) => setTheme(t)">
    <button type="button" class="icon-btn" :aria-label="$t('theme.label', { name: $t(`theme.${current.value}`) })" :title="$t('theme.label', { name: $t(`theme.${current.value}`) })">
      <el-icon :size="18"><component :is="current.icon" /></el-icon>
    </button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item v-for="o in options" :key="o.value" :command="o.value" :class="{ 'is-current': o.value === themePreference }">
          <el-icon><component :is="o.icon" /></el-icon>{{ $t(`theme.${o.value}`) }}
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<style scoped>
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
  transition: background-color var(--bk-motion), color var(--bk-motion);
}
.icon-btn:hover {
  background: var(--bk-hover);
  color: var(--bk-on-surface);
}
.is-current {
  color: var(--el-color-primary);
  font-weight: 600;
}
</style>
