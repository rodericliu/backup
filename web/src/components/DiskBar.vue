<script setup lang="ts">
import { computed } from 'vue'
import { bytes } from '@/format'
import type { DiskUsage } from '@/types'

const props = defineProps<{ disk: DiskUsage }>()
const ratio = computed(() => (props.disk.total ? (props.disk.total - props.disk.free) / props.disk.total : 0))
const alert = computed(() => ratio.value > 0.9)
</script>

<template>
  <div class="disk">
    <div class="label">
      <span>{{ disk.role === 'backup' ? $t('disk.backup') : $t('disk.data') }} <span class="mono muted">{{ disk.path }}</span></span>
      <span class="bk-num" :class="{ danger: alert }">{{ bytes(disk.total - disk.free) }} / {{ bytes(disk.total) }} · {{ (ratio * 100).toFixed(1) }}%</span>
    </div>
    <el-progress :percentage="Math.min(100, ratio * 100)" :status="alert ? 'exception' : undefined" :stroke-width="8" :show-text="false" />
  </div>
</template>

<style scoped>
.disk {
  margin-bottom: 8px;
}
.label {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  margin-bottom: 4px;
}
</style>
