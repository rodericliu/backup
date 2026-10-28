<script setup lang="ts">
import { hostStatusTag, hostStatusText } from '@/format'

defineProps<{ status: string; paused?: boolean; compatible?: boolean }>()
</script>

<template>
  <span class="host-status">
    <el-tag :type="hostStatusTag[status]" size="small" effect="dark" disable-transitions>{{ hostStatusText[status] ?? status }}</el-tag>
    <el-tag v-if="paused" type="warning" size="small" disable-transitions>已暂停调度</el-tag>
    <el-tag v-if="compatible === false" type="danger" size="small" disable-transitions>API 版本不兼容</el-tag>
  </span>
</template>

<style scoped>
.host-status {
  display: inline-flex;
  gap: 4px;
}
</style>
