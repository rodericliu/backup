<script setup lang="ts">
defineProps<{ size?: number }>()
</script>

<template>
  <svg :width="size ?? 28" :height="size ?? 28" viewBox="0 0 32 32" fill="none" aria-hidden="true">
    <ellipse cx="14" cy="7.5" rx="9" ry="3.5" stroke="currentColor" stroke-width="2" />
    <path d="M5 7.5v15c0 1.93 4.03 3.5 9 3.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
    <path d="M23 7.5V14" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
    <path d="M5 15c0 1.93 4.03 3.5 9 3.5 1.1 0 2.15-.08 3.12-.22" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
    <path d="M28 23.5a5 5 0 1 1-1.46-3.54" stroke="var(--el-color-primary)" stroke-width="2" stroke-linecap="round" />
    <path d="M27.5 17.5v3h-3" stroke="var(--el-color-primary)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
</template>
