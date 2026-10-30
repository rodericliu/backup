<script setup lang="ts">
import { computed } from 'vue'
import { locale, locales, setLocale, type Locale } from '@/i18n'

const current = computed(() => locales.find((l) => l.value === locale.value) ?? locales[0])
</script>

<template>
  <el-dropdown trigger="click" placement="bottom-end" @command="(l: Locale) => setLocale(l)">
    <button type="button" class="icon-btn" :aria-label="$t('locale.switch', { name: current.label })" :title="$t('locale.switch', { name: current.label })">
      <span class="short">{{ current.short }}</span>
    </button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item v-for="l in locales" :key="l.value" :command="l.value" :class="{ 'is-current': l.value === locale }" :lang="l.value">
          {{ l.label }}
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
.short {
  font-size: 0.8125rem;
  font-weight: 600;
}
.is-current {
  color: var(--el-color-primary);
  font-weight: 600;
}
</style>
