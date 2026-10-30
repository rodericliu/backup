<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, Close, Clock, Delete, Edit, Plus, Promotion, VideoPause } from '@element-plus/icons-vue'
import { api, errorMessage } from '@/api'
import { ago } from '@/format'
import type { AssistantSession, Project } from '@/types'
import {
  assistantOpen,
  assistantWidth,
  chatContext,
  clearContext,
  current,
  deleteSession,
  draft,
  info,
  loadError,
  loadSessions,
  model,
  newSession,
  openSession,
  renameSession,
  send,
  sessions,
  stop,
  streaming,
} from '@/composables/assistant'
import ChatMessage from './ChatMessage.vue'
import { t } from '@/i18n'

const input = ref('')
const inputRef = ref<{ focus: () => void }>()
const scroller = ref<HTMLElement>()
const historyOpen = ref(false)

// Suggestions name a real project.
const example = ref('')
const suggestions = computed(() => {
  const name = example.value || t('assistant.someProject')
  return [t('assistant.suggestToday'), t('assistant.suggestWeek'), t('assistant.suggestWhere', { name }), t('assistant.suggestRestore', { name })]
})

onMounted(async () => {
  loadSessions()
  takeDraft()
  try {
    const projects = await api.get<Project[]>('/projects')
    if (projects.length) example.value = projects[0].name
  } catch {
    // keep the placeholder
  }
})

function takeDraft() {
  if (!draft.value) return
  input.value = draft.value
  draft.value = ''
  nextTick(() => inputRef.value?.focus())
}
watch(draft, takeDraft)

// Keep the newest output in view unless the user scrolled up.
let pinned = true
function onScroll() {
  const el = scroller.value
  if (el) pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 80
}
const tail = computed(() => {
  const m = current.value?.messages.at(-1)
  return m ? `${current.value?.messages.length}:${m.content.length}:${m.tool_calls.length}:${m.tool_calls.filter((c) => c.pending).length}` : ''
})
watch(tail, () => {
  if (pinned) nextTick(() => scroller.value?.scrollTo({ top: scroller.value.scrollHeight }))
})
watch(
  () => current.value?.id,
  () => {
    pinned = true
    nextTick(() => scroller.value?.scrollTo({ top: scroller.value.scrollHeight }))
  },
)

function submit() {
  const q = input.value.trim()
  if (!q || streaming.value) return
  input.value = ''
  pinned = true
  send(q)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    submit()
  }
}

function useSuggestion(s: string) {
  input.value = s
  nextTick(() => inputRef.value?.focus())
}

async function pick(s: AssistantSession) {
  historyOpen.value = false
  try {
    await openSession(s.id)
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

async function rename(s: AssistantSession) {
  try {
    const { value } = await ElMessageBox.prompt(t('assistant.sessionName'), t('assistant.rename'), {
      inputValue: s.title,
      confirmButtonText: t('common.save'),
    })
    if (value?.trim()) await renameSession(s, value)
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') ElMessage.error(errorMessage(e))
  }
}

async function remove(s: AssistantSession) {
  try {
    await deleteSession(s)
  } catch (e) {
    ElMessage.error(errorMessage(e))
  }
}

const contextText = computed(() => {
  const c = chatContext.value
  const parts = []
  if (c.job_id) parts.push(t('assistant.ctxJob', { id: `${c.job_id.slice(0, 8)}…` }))
  if (c.host_id) parts.push(t('assistant.ctxHost', { id: c.host_id }))
  if (c.project_id) parts.push(t('assistant.ctxProject', { id: c.project_id }))
  return parts.join(t('common.listSep'))
})

// Drag the left edge to resize.
function startResize(e: PointerEvent) {
  const startX = e.clientX
  const startW = assistantWidth.value
  const move = (ev: PointerEvent) => {
    assistantWidth.value = Math.min(Math.max(startW + startX - ev.clientX, 360), Math.min(900, window.innerWidth - 480))
  }
  const up = () => {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
    document.body.style.userSelect = ''
  }
  document.body.style.userSelect = 'none'
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
</script>

<template>
  <aside id="assistant-drawer" class="drawer" :style="{ '--w': assistantWidth + 'px' }" :aria-label="$t('nav.assistant')">
    <div class="resize" :title="$t('assistant.resize')" @pointerdown.prevent="startResize" />
    <header class="head">
      <el-icon class="head-icon" :size="18"><ChatDotRound /></el-icon>
      <div class="head-title">
        <div class="title">{{ $t('nav.assistantShort') }}</div>
        <div class="subtitle">{{ current?.title || $t('assistant.newSession') }}</div>
      </div>
      <el-popover v-model:visible="historyOpen" placement="bottom-end" :width="320" trigger="click" popper-class="assistant-history">
        <template #reference>
          <button type="button" class="icon-btn" :title="$t('assistant.history')" :aria-label="$t('assistant.history')" @click="loadSessions"><el-icon :size="18"><Clock /></el-icon></button>
        </template>
        <div class="history-title">{{ $t('assistant.history') }}</div>
        <el-alert v-if="loadError" :title="loadError" type="error" :closable="false" />
        <el-empty v-else-if="!sessions.length" :description="$t('assistant.noSessions')" :image-size="48" />
        <div class="history-list">
          <div v-for="s in sessions" :key="s.id" class="history-item" :class="{ active: current?.id === s.id }" @click="pick(s)">
            <div class="history-text">
              <div class="history-name">{{ s.title || $t('assistant.untitled') }}</div>
              <div class="history-time">{{ ago(s.updated_at) }}</div>
            </div>
            <button type="button" class="mini" :title="$t('assistant.rename')" @click.stop="rename(s)"><el-icon><Edit /></el-icon></button>
            <el-popconfirm :title="$t('assistant.deleteSession')" @confirm="remove(s)">
              <template #reference>
                <button type="button" class="mini danger" :title="$t('common.delete')" @click.stop><el-icon><Delete /></el-icon></button>
              </template>
            </el-popconfirm>
          </div>
        </div>
      </el-popover>
      <button type="button" class="icon-btn" :title="$t('assistant.newSession')" :aria-label="$t('assistant.newSession')" :disabled="streaming" @click="newSession"><el-icon :size="18"><Plus /></el-icon></button>
      <button type="button" class="icon-btn" :title="$t('assistant.close')" :aria-label="$t('assistant.closeAssistant')" @click="assistantOpen = false"><el-icon :size="18"><Close /></el-icon></button>
    </header>

    <div ref="scroller" class="body" @scroll="onScroll">
      <div v-if="!current?.messages.length" class="welcome">
        <div class="welcome-icon"><el-icon :size="24"><ChatDotRound /></el-icon></div>
        <h3>{{ $t('assistant.welcomeTitle') }}</h3>
        <p>{{ $t('assistant.welcome') }}</p>
        <div class="suggestions">
          <button v-for="s in suggestions" :key="s" type="button" class="suggestion" @click="useSuggestion(s)">{{ s }}</button>
        </div>
      </div>
      <ChatMessage
        v-for="(m, i) in current?.messages ?? []"
        :key="m.id > 0 ? m.id : 'live-' + i"
        :message="m"
        :live="streaming && i === (current?.messages.length ?? 0) - 1"
      />
    </div>

    <footer class="foot">
      <div v-if="contextText" class="context">
        <span>{{ $t('assistant.viewing', { ctx: contextText }) }}</span>
        <button type="button" class="mini" :title="$t('assistant.noContext')" @click="clearContext"><el-icon><Close /></el-icon></button>
      </div>
      <div class="composer">
        <el-input
          ref="inputRef"
          v-model="input"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 8 }"
          resize="none"
          maxlength="4000"
          :placeholder="$t('assistant.placeholder')"
          @keydown="onKeydown"
        />
        <div class="composer-bar">
          <el-select v-if="(info?.models.length ?? 0) > 1" v-model="model" size="small" class="model" :disabled="streaming">
            <el-option v-for="m in info?.models" :key="m" :label="m" :value="m" />
          </el-select>
          <span v-else class="model-name">{{ model }}</span>
          <span class="spacer" />
          <el-button v-if="streaming" size="small" :icon="VideoPause" @click="stop">{{ $t('assistant.stop') }}</el-button>
          <el-button v-else type="primary" size="small" :icon="Promotion" :disabled="!input.trim()" @click="submit">{{ $t('assistant.send') }}</el-button>
        </div>
      </div>
    </footer>
  </aside>
</template>

<style scoped>
.drawer {
  position: fixed;
  z-index: 12;
  inset-block: var(--bk-navbar-height) 0;
  inset-inline-end: 0;
  display: flex;
  flex-direction: column;
  inline-size: var(--w);
  max-inline-size: 100vw;
  background: var(--bk-surface);
  border-inline-start: 1px solid var(--bk-border);
}
.resize {
  position: absolute;
  z-index: 1;
  inset-block: 0;
  inset-inline-start: -3px;
  inline-size: 6px;
  cursor: col-resize;
}
.resize:hover {
  background: rgba(var(--bk-primary-rgb), 0.3);
}
.head {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  padding: 10px 8px 10px 16px;
  border-block-end: 1px solid var(--bk-border);
}
.head-icon {
  color: var(--el-color-primary);
  margin-inline-end: 6px;
}
.head-title {
  flex: 1;
  min-width: 0;
}
.title {
  font-weight: 600;
}
.subtitle {
  overflow: hidden;
  color: var(--bk-secondary);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.icon-btn {
  display: inline-grid;
  place-items: center;
  inline-size: 34px;
  block-size: 34px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--bk-secondary);
  cursor: pointer;
}
.icon-btn:hover:not(:disabled) {
  background: var(--bk-hover);
  color: var(--bk-on-surface);
}
.icon-btn:disabled {
  opacity: 0.4;
  cursor: default;
}
.body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 16px;
}
.welcome {
  padding: 24px 4px;
  text-align: center;
}
.welcome-icon {
  display: inline-grid;
  place-items: center;
  inline-size: 48px;
  block-size: 48px;
  margin-bottom: 12px;
  border: 1px solid var(--bk-border);
  border-radius: 12px;
  background: rgba(var(--bk-primary-rgb), 0.08);
  color: var(--el-color-primary);
}
.welcome h3 {
  margin: 0 0 6px;
  font-size: 15px;
}
.welcome p {
  margin: 0 auto 18px;
  max-width: 34em;
  color: var(--bk-secondary);
  font-size: 13px;
  line-height: 1.6;
}
.suggestions {
  display: flex;
  flex-direction: column;
  gap: 8px;
  text-align: left;
}
.suggestion {
  padding: 10px 12px;
  border: 1px solid var(--bk-border);
  border-radius: 10px;
  background: var(--bk-surface);
  color: var(--bk-on-surface);
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
  transition: border-color var(--bk-motion), background-color var(--bk-motion);
}
.suggestion:hover {
  border-color: rgba(var(--bk-primary-rgb), 0.5);
  background: rgba(var(--bk-primary-rgb), 0.05);
}
.foot {
  flex-shrink: 0;
  padding: 10px 12px 12px;
  border-block-start: 1px solid var(--bk-border);
}
.context {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  padding: 4px 4px 4px 10px;
  border-radius: 6px;
  background: rgba(var(--bk-primary-rgb), 0.08);
  color: var(--el-color-primary);
  font-size: 12px;
}
.composer {
  border: 1px solid var(--bk-border);
  border-radius: 10px;
  background: var(--bk-surface);
  transition: border-color var(--bk-motion);
}
.composer:focus-within {
  border-color: var(--el-color-primary);
}
.composer :deep(.el-textarea__inner) {
  padding: 10px 12px 4px;
  border: none;
  box-shadow: none;
  background: transparent;
  font-size: 14px;
}
.composer-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px 8px 12px;
}
.model {
  width: 150px;
}
.model-name {
  color: var(--bk-secondary);
  font-size: 12px;
}
.spacer {
  flex: 1;
}
.mini {
  display: inline-grid;
  place-items: center;
  flex-shrink: 0;
  inline-size: 24px;
  block-size: 24px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: inherit;
  cursor: pointer;
}
.mini:hover {
  background: var(--bk-hover);
}
.mini.danger:hover {
  color: var(--el-color-danger);
}
.history-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.history-list {
  max-height: 360px;
  overflow-y: auto;
  margin: 0 -6px;
}
.history-item {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px;
  border-radius: 6px;
  cursor: pointer;
}
.history-item:hover {
  background: var(--bk-hover);
}
.history-item.active {
  background: rgba(var(--bk-primary-rgb), 0.09);
  color: var(--el-color-primary);
}
.history-text {
  flex: 1;
  min-width: 0;
}
.history-name {
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.history-time {
  color: var(--bk-secondary);
  font-size: 11px;
}
@media (max-width: 767px) {
  .drawer {
    z-index: 15;
    inset-block-start: 0;
    inline-size: 100vw;
  }
  .resize {
    display: none;
  }
}
</style>
