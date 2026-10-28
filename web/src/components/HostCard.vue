<script setup lang="ts">
import { computed } from 'vue'
import { ago, bytes, goDuration, rate, shortTime, typeText } from '@/format'
import type { Host, Progress } from '@/types'
import HostStatus from './HostStatus.vue'
import DiskBar from './DiskBar.vue'

// HostCard shows what a host is doing (PRD D-1, D-4, H-4). When the host is
// offline the last known state is shown as such.
const props = defineProps<{ host: Host; progress?: Progress; queueLimit?: number }>()

const current = computed(() => props.host.state?.current)
const progress = computed(() => props.progress ?? props.host.state?.progress)
const offline = computed(() => props.host.status !== 'online')
const queue = computed(() => (props.host.state?.queue ?? []).slice(0, props.queueLimit ?? 3))
const more = computed(() => (props.host.state?.queue?.length ?? 0) - queue.value.length)
</script>

<template>
  <el-card shadow="hover" class="host-card" :class="{ offline }">
    <template #header>
      <div class="head">
        <router-link :to="{ name: 'hosts', query: { id: host.id } }" class="name">{{ host.name }}</router-link>
        <HostStatus :status="host.status" :paused="host.paused" :compatible="host.compatible" />
      </div>
      <div class="sub muted">
        {{ host.ip || '-' }} · agent {{ host.agent_version || '-' }} · 心跳 {{ ago(host.last_heartbeat_at) }}
      </div>
    </template>

    <div class="section">
      <div class="section-title">{{ offline && current ? '离线前最后已知' : '当前任务' }}</div>
      <div v-if="current" class="current" :class="{ stale: offline }">
        <div>
          <b>{{ current.project }}</b> {{ typeText[current.type] }}备份
          <span class="muted">（计划 {{ shortTime(current.scheduled_at) }}）</span>
        </div>
        <div v-if="progress" class="progress">
          已写入 <b>{{ bytes(progress.compressed_bytes) }}</b> · {{ rate(progress.bytes_per_second) }} · 已耗时
          {{ goDuration(progress.elapsed) }}
        </div>
      </div>
      <div v-else class="muted">空闲</div>
    </div>

    <div class="section">
      <div class="section-title">队列</div>
      <div v-if="queue.length" class="queue">
        <el-tag v-for="j in queue" :key="j.id" size="small" type="info" disable-transitions>
          {{ shortTime(j.scheduled_at) }} {{ j.project }} {{ typeText[j.type] }}
        </el-tag>
        <span v-if="more > 0" class="muted">等 {{ more }} 个</span>
      </div>
      <div v-else class="muted">无</div>
    </div>

    <div v-if="host.metrics?.disks?.length" class="section">
      <div class="section-title">磁盘</div>
      <DiskBar v-for="d in host.metrics.disks" :key="d.path" :disk="d" />
    </div>
  </el-card>
</template>

<style scoped>
.head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.name {
  font-weight: 600;
  font-size: 16px;
  color: inherit;
  text-decoration: none;
}
.sub {
  font-size: 12px;
  margin-top: 4px;
}
.offline :deep(.el-card__header) {
  background: var(--el-color-danger-light-9);
}
.section {
  margin-bottom: 12px;
}
.section-title {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}
.current .progress {
  margin-top: 4px;
  font-size: 13px;
}
.current.stale {
  color: var(--el-text-color-secondary);
}
.queue {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: center;
}
</style>
