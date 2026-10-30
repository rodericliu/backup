<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowRight, CircleCheck, CircleClose, Loading } from '@element-plus/icons-vue'
import type { AssistantToolCall } from '@/types'
import { keyed } from '@/i18n'

const props = defineProps<{ call: AssistantToolCall }>()
const open = ref(false)

const labels = keyed('assistant.tools')

function pretty(s: string) {
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

const args = computed(() => {
  try {
    const a = JSON.parse(props.call.arguments || '{}') as Record<string, unknown>
    return Object.entries(a)
      .filter(([, v]) => v !== '' && v !== null && v !== undefined)
      .map(([k, v]) => `${k}=${Array.isArray(v) ? v.join(',') : v}`)
      .join(' ')
  } catch {
    return props.call.arguments
  }
})
</script>

<template>
  <div class="tool" :class="{ open, error: call.error }">
    <button type="button" class="tool-head" :aria-expanded="open" @click="open = !open">
      <el-icon class="chevron"><ArrowRight /></el-icon>
      <span class="name">{{ labels[call.name] ?? call.name }}</span>
      <span class="args mono">{{ args }}</span>
      <el-icon v-if="call.pending" class="state is-loading"><Loading /></el-icon>
      <el-icon v-else-if="call.error" class="state bad"><CircleClose /></el-icon>
      <el-icon v-else class="state ok"><CircleCheck /></el-icon>
    </button>
    <div v-if="open" class="tool-body">
      <div class="section-label">{{ $t('assistant.toolArgs', { name: call.name }) }}</div>
      <pre class="code">{{ pretty(call.arguments || '{}') }}</pre>
      <div class="section-label">{{ $t('assistant.toolResult') }}</div>
      <pre class="code result">{{ call.pending ? $t('assistant.toolRunning') : pretty(call.result) }}</pre>
    </div>
  </div>
</template>

<style scoped>
.tool {
  border: 1px solid var(--bk-border);
  border-radius: 8px;
  background: var(--bk-subtle);
  font-size: 12px;
}
.tool + .tool {
  margin-top: 6px;
}
.tool-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 10px;
  border: none;
  background: transparent;
  color: var(--bk-on-surface);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.chevron {
  color: var(--bk-secondary);
  transition: transform var(--bk-motion);
}
.open .chevron {
  transform: rotate(90deg);
}
.name {
  flex-shrink: 0;
  font-weight: 600;
}
.args {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  color: var(--bk-secondary);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.state.ok {
  color: var(--el-color-success);
}
.state.bad {
  color: var(--el-color-danger);
}
.tool-body {
  padding: 4px 10px 10px;
}
.tool-body .section-label {
  margin: 8px 0 4px;
}
.tool-body pre.code {
  max-height: 260px;
  background: var(--bk-surface);
}
</style>
