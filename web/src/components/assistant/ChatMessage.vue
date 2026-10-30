<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowRight, WarningFilled } from '@element-plus/icons-vue'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import type { AssistantMessage } from '@/types'
import ToolCallBlock from './ToolCallBlock.vue'
import { t } from '@/i18n'

const props = defineProps<{ message: AssistantMessage; live?: boolean }>()
const router = useRouter()
const showReasoning = ref(false)

const escape = (s: string) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)

const md = new Marked({
  gfm: true,
  breaks: true,
  renderer: {
    code({ text, lang }) {
      const label = lang ? `<span class="code-lang">${escape(lang)}</span>` : ''
      return `<div class="code-block">${label}<button type="button" class="code-copy">${escape(t('common.copy'))}</button><pre><code>${escape(text)}</code></pre></div>`
    },
  },
})

DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    const href = node.getAttribute('href') ?? ''
    if (!href.startsWith('/')) {
      node.setAttribute('target', '_blank')
      node.setAttribute('rel', 'noopener noreferrer')
    }
  }
})

const html = computed(() => DOMPurify.sanitize(md.parse(props.message.content, { async: false }) as string))

async function onClick(e: MouseEvent) {
  const el = e.target as HTMLElement
  const copy = el.closest('.code-copy')
  if (copy) {
    const code = copy.parentElement?.querySelector('pre')?.textContent ?? ''
    try {
      await navigator.clipboard.writeText(code)
      ElMessage.success(t('common.copied'))
    } catch {
      ElMessage.warning(t('common.copyFailed'))
    }
    return
  }
  const a = el.closest('a')
  const href = a?.getAttribute('href')
  if (a && href?.startsWith('/') && !e.metaKey && !e.ctrlKey) {
    e.preventDefault()
    router.push(href)
  }
}

const tokens = computed(() => {
  const m = props.message
  return m.input_tokens ? `${m.model ?? ''} · ${t('assistant.tokens', { input: m.input_tokens, output: m.output_tokens ?? 0 })}` : m.model
})
</script>

<template>
  <div v-if="message.role === 'user'" class="msg user">
    <div class="bubble">{{ message.content }}</div>
  </div>
  <div v-else class="msg assistant">
    <div v-if="message.reasoning" class="reasoning">
      <button type="button" class="reasoning-head" @click="showReasoning = !showReasoning">
        <el-icon :class="{ rot: showReasoning }"><ArrowRight /></el-icon>{{ live && !message.content ? $t('assistant.thinking') : $t('assistant.reasoning') }}
      </button>
      <div v-if="showReasoning" class="reasoning-body">{{ message.reasoning }}</div>
    </div>
    <div v-if="message.tool_calls.length" class="tools">
      <ToolCallBlock v-for="c in message.tool_calls" :key="c.id" :call="c" />
    </div>
    <!-- eslint-disable-next-line vue/no-v-html -- sanitized by DOMPurify -->
    <div v-if="message.content" class="markdown" @click="onClick" v-html="html" />
    <div v-else-if="live && !message.error" class="typing"><span /><span /><span /></div>
    <el-alert v-if="message.error" :title="message.error" type="error" :closable="false" show-icon class="err">
      <template #icon><el-icon><WarningFilled /></el-icon></template>
    </el-alert>
    <div v-if="!live && tokens" class="meta">{{ tokens }}</div>
  </div>
</template>

<style scoped>
.msg {
  margin-bottom: 18px;
}
.user {
  display: flex;
  justify-content: flex-end;
}
.bubble {
  max-width: 85%;
  padding: 8px 12px;
  border-radius: 12px 12px 4px 12px;
  background: rgba(var(--bk-primary-rgb), 0.12);
  color: var(--bk-on-surface);
  white-space: pre-wrap;
  word-break: break-word;
}
.tools {
  margin-bottom: 10px;
}
.reasoning {
  margin-bottom: 8px;
}
.reasoning-head {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: none;
  background: none;
  color: var(--bk-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.reasoning-head .el-icon {
  transition: transform var(--bk-motion);
}
.reasoning-head .rot {
  transform: rotate(90deg);
}
.reasoning-body {
  margin-top: 6px;
  padding-left: 10px;
  border-left: 2px solid var(--bk-border);
  color: var(--bk-secondary);
  font-size: 12px;
  white-space: pre-wrap;
}
.err {
  margin-top: 8px;
}
.meta {
  margin-top: 6px;
  color: var(--bk-secondary);
  font-size: 11px;
}
.typing {
  display: flex;
  gap: 4px;
  padding: 6px 0;
}
.typing span {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--bk-secondary);
  animation: blink 1.2s infinite ease-in-out;
}
.typing span:nth-child(2) {
  animation-delay: 0.2s;
}
.typing span:nth-child(3) {
  animation-delay: 0.4s;
}
@keyframes blink {
  0%,
  80%,
  100% {
    opacity: 0.25;
  }
  40% {
    opacity: 1;
  }
}

.markdown {
  font-size: 14px;
  line-height: 1.7;
  word-break: break-word;
}
.markdown :deep(> :first-child) {
  margin-top: 0;
}
.markdown :deep(> :last-child) {
  margin-bottom: 0;
}
.markdown :deep(p),
.markdown :deep(ul),
.markdown :deep(ol),
.markdown :deep(table),
.markdown :deep(.code-block),
.markdown :deep(blockquote) {
  margin: 0 0 10px;
}
.markdown :deep(h1),
.markdown :deep(h2),
.markdown :deep(h3),
.markdown :deep(h4) {
  margin: 14px 0 8px;
  font-size: 15px;
  font-weight: 600;
}
.markdown :deep(ul),
.markdown :deep(ol) {
  padding-left: 20px;
}
.markdown :deep(code) {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--bk-subtle);
  color: var(--bk-code);
  font-family: var(--bk-font-data);
  font-size: 12px;
}
.markdown :deep(.code-block) {
  position: relative;
}
.markdown :deep(.code-block pre) {
  margin: 0;
  padding: 12px 14px;
  overflow: auto;
  border: 1px solid var(--bk-border);
  border-radius: 8px;
  background: var(--bk-subtle);
  font-family: var(--bk-font-data);
  font-size: 12px;
  line-height: 1.6;
}
.markdown :deep(.code-block pre code) {
  padding: 0;
  background: none;
  color: var(--bk-on-surface);
}
.markdown :deep(.code-copy) {
  position: absolute;
  top: 6px;
  right: 6px;
  padding: 2px 8px;
  border: 1px solid var(--bk-border);
  border-radius: 6px;
  background: var(--bk-surface);
  color: var(--bk-secondary);
  font-size: 11px;
  cursor: pointer;
  opacity: 0;
  transition: opacity var(--bk-motion);
}
.markdown :deep(.code-block:hover .code-copy),
.markdown :deep(.code-copy:focus-visible) {
  opacity: 1;
}
.markdown :deep(.code-lang) {
  position: absolute;
  top: -9px;
  left: 10px;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--bk-surface);
  color: var(--bk-secondary);
  font-size: 10px;
}
.markdown :deep(table) {
  display: block;
  max-width: 100%;
  overflow-x: auto;
  border-collapse: collapse;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
.markdown :deep(th),
.markdown :deep(td) {
  padding: 6px 10px;
  border: 1px solid var(--bk-border);
  text-align: left;
  white-space: nowrap;
}
.markdown :deep(th) {
  background: var(--bk-table-header);
  font-weight: 600;
}
.markdown :deep(blockquote) {
  padding-left: 10px;
  border-left: 3px solid var(--bk-border);
  color: var(--bk-secondary);
}
.markdown :deep(a) {
  color: var(--el-color-primary);
}
</style>
