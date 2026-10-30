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
  <article class="bk-panel host-card" :class="{ offline }">
    <header class="head">
      <div class="title-row">
        <router-link :to="{ name: 'hosts', query: { id: host.id } }" class="name">{{ host.name }}</router-link>
        <HostStatus :status="host.status" :paused="host.paused" :compatible="host.compatible" />
      </div>
      <div class="sub">
        <span class="bk-data">{{ host.ip || '-' }}</span>
        <span class="dot" />agent <span class="bk-data">{{ host.agent_version || '-' }}</span>
        <span class="dot" />{{ $t('hostCard.heartbeat', { ago: ago(host.last_heartbeat_at) }) }}
      </div>
    </header>

    <div class="body">
      <div class="section">
        <div class="section-label">{{ offline && current ? $t('hostCard.lastKnown') : $t('hostCard.current') }}</div>
        <div v-if="current" class="current" :class="{ stale: offline }">
          <div>
            <b>{{ current.project }}</b> {{ $t('common.typeBackup', { type: typeText[current.type] }) }}
            <span class="muted">{{ $t('hostCard.scheduledAt', { time: shortTime(current.scheduled_at) }) }}</span>
          </div>
          <div v-if="progress" class="progress">
            {{ $t('progress.written') }} <b class="bk-num">{{ bytes(progress.compressed_bytes) }}</b>
            <span class="dot" />{{ rate(progress.bytes_per_second) }}
            <span class="dot" />{{ $t('progress.elapsed', { d: goDuration(progress.elapsed) }) }}
          </div>
        </div>
        <div v-else class="muted">{{ $t('hostCard.idle') }}</div>
      </div>

      <div class="section">
        <div class="section-label">{{ $t('hostCard.queue') }}</div>
        <div v-if="queue.length" class="queue">
          <el-tag v-for="j in queue" :key="j.id" size="small" type="info" disable-transitions>
            <span class="bk-num">{{ shortTime(j.scheduled_at) }}</span> {{ j.project }} {{ typeText[j.type] }}
          </el-tag>
          <span v-if="more > 0" class="muted">{{ $t('hostCard.more', { n: more }) }}</span>
        </div>
        <div v-else class="muted">{{ $t('common.none') }}</div>
      </div>

      <div v-if="host.metrics?.disks?.length" class="section">
        <div class="section-label">{{ $t('hostCard.disks') }}</div>
        <DiskBar v-for="d in host.metrics.disks" :key="d.path" :disk="d" />
      </div>
    </div>
  </article>
</template>

<style scoped>
.host-card {
  display: flex;
  flex-direction: column;
  transition: border-color var(--bk-motion);
}
.host-card:hover {
  border-color: rgba(var(--bk-primary-rgb), 0.4);
}
.head {
  padding: 16px var(--bk-panel-padding) 14px;
  border-block-end: 1px solid var(--bk-border);
}
.offline .head {
  background: var(--el-color-danger-light-9);
}
.title-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}
.name {
  font-weight: 600;
  font-size: 1rem;
  color: var(--bk-on-surface);
}
.sub {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  margin-top: 4px;
  color: var(--bk-secondary);
  font-size: 12px;
}
.dot::before {
  content: '·';
  margin-inline: 6px;
}
.body {
  padding: 14px var(--bk-panel-padding) 6px;
}
.section {
  margin-bottom: 14px;
}
.section-label {
  margin-bottom: 6px;
}
.current .progress {
  margin-top: 4px;
  font-size: 13px;
}
.current.stale {
  color: var(--bk-secondary);
}
.queue {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: center;
}
</style>
