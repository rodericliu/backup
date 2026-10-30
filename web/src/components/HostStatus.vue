<script setup lang="ts">
import { hostStatusTag, hostStatusText } from '@/format'

defineProps<{ status: string; paused?: boolean; compatible?: boolean }>()
</script>

<template>
  <span class="host-status">
    <el-tag :type="hostStatusTag[status]" size="small" disable-transitions><i class="status-dot" />{{ hostStatusText[status] ?? status }}</el-tag>
    <el-tag v-if="paused" type="warning" size="small" disable-transitions>{{ $t('hostStatus.paused') }}</el-tag>
    <el-tag v-if="compatible === false" type="danger" size="small" disable-transitions>{{ $t('hostStatus.incompatible') }}</el-tag>
  </span>
</template>

<style scoped>
.host-status {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}
.status-dot {
  display: inline-block;
  inline-size: 6px;
  block-size: 6px;
  margin-inline-end: 6px;
  border-radius: 50%;
  background: currentColor;
  vertical-align: 1px;
}
</style>
