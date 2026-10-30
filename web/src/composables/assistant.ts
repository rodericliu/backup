import { computed, reactive, ref, watch } from 'vue'
import { api, errorMessage, responseError } from '@/api'
import { locale, t } from '@/i18n'
import type { AssistantInfo, AssistantMessage, AssistantSession, AssistantSessionDetail, ChatContext, ChatEvent } from '@/types'

// State of the AI assistant drawer, shared by the layout, the drawer and
// pages that set what the user is looking at.

const openKey = 'backup-assistant-open'
const widthKey = 'backup-assistant-width'
const modelKey = 'backup-assistant-model'

export const assistantOpen = ref(localStorage.getItem(openKey) === '1')
export const assistantWidth = ref(Number(localStorage.getItem(widthKey)) || 440)
watch(assistantOpen, (v) => localStorage.setItem(openKey, v ? '1' : '0'))
watch(assistantWidth, (v) => localStorage.setItem(widthKey, String(Math.round(v))))

export const info = ref<AssistantInfo | null>(null)
export const enabled = computed(() => !!info.value?.enabled)
export const model = ref(localStorage.getItem(modelKey) ?? '')
watch(model, (v) => localStorage.setItem(modelKey, v))

export const sessions = ref<AssistantSession[]>([])
export const current = ref<AssistantSessionDetail | null>(null)
export const streaming = ref(false)
export const loadError = ref('')

// pageContext is what the current page shows, sent with questions.
export const pageContext = reactive<ChatContext>({})
// pinnedContext was given with ask() and outlives the page that set it.
export const pinnedContext = reactive<ChatContext>({})
export const chatContext = computed<ChatContext>(() => ({ ...pageContext, ...pinnedContext }))

export function clearContext() {
  for (const c of [pageContext, pinnedContext]) {
    delete c.job_id
    delete c.host_id
    delete c.project_id
  }
}

// draft is text put into the input box by pages ("ask the assistant").
export const draft = ref('')

let controller: AbortController | null = null

export async function loadInfo() {
  if (info.value) return
  try {
    info.value = await api.get<AssistantInfo>('/assistant/config')
    if (!info.value.models.includes(model.value)) model.value = info.value.models[0] ?? ''
  } catch {
    info.value = { enabled: false, models: [] }
  }
}

export async function loadSessions() {
  try {
    sessions.value = await api.get<AssistantSession[]>('/assistant/sessions')
    loadError.value = ''
  } catch (e) {
    loadError.value = errorMessage(e)
  }
}

export async function openSession(id: number) {
  if (streaming.value) return
  current.value = await api.get<AssistantSessionDetail>(`/assistant/sessions/${id}`)
}

export function newSession() {
  if (streaming.value) return
  current.value = null
}

export async function renameSession(s: AssistantSession, title: string) {
  const updated = await api.patch<AssistantSession>(`/assistant/sessions/${s.id}`, { title })
  s.title = updated.title
  if (current.value?.id === s.id) current.value.title = updated.title
}

export async function deleteSession(s: AssistantSession) {
  await api.del(`/assistant/sessions/${s.id}`)
  sessions.value = sessions.value.filter((x) => x.id !== s.id)
  if (current.value?.id === s.id) current.value = null
}

// ask opens the drawer with a question in the input box.
export function ask(question: string, ctx?: ChatContext) {
  if (ctx) Object.assign(pinnedContext, ctx)
  draft.value = question
  assistantOpen.value = true
}

export function stop() {
  controller?.abort()
}

// send asks a question in the current session, creating one if needed,
// and streams the answer into it.
export async function send(content: string) {
  content = content.trim()
  if (!content || streaming.value) return
  streaming.value = true
  const now = new Date().toISOString()
  const live = reactive<AssistantMessage>({
    id: 0, session_id: 0, role: 'assistant', content: '', reasoning: '', tool_calls: [], created_at: now,
  })
  try {
    if (!current.value) {
      const s = await api.post<AssistantSession>('/assistant/sessions')
      current.value = { ...s, messages: [] }
    }
    const sess = current.value
    const question: AssistantMessage = { id: -1, session_id: sess.id, role: 'user', content, tool_calls: [], created_at: now }
    sess.messages.push(question, live)
    live.session_id = sess.id

    controller = new AbortController()
    const path = `/assistant/sessions/${sess.id}/messages`
    const resp = await fetch('/api/v1' + path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'Accept-Language': locale.value },
      body: JSON.stringify({ content, model: model.value || undefined, context: chatContext.value }),
      signal: controller.signal,
    })
    if (!resp.ok) throw await responseError(resp, path)
    await readEvents(resp, (ev) => {
      switch (ev.type) {
        case 'user_message':
          if (ev.message) Object.assign(question, ev.message)
          break
        case 'content':
          live.content += ev.content ?? ''
          break
        case 'reasoning':
          live.reasoning = (live.reasoning ?? '') + (ev.content ?? '')
          break
        case 'tool_call':
          if (ev.tool_call) live.tool_calls.push({ ...ev.tool_call, result: '', pending: true })
          break
        case 'tool_result': {
          const c = live.tool_calls.find((t) => t.id === ev.tool_result?.id)
          if (c && ev.tool_result) Object.assign(c, { result: ev.tool_result.content, error: ev.tool_result.error, pending: false })
          break
        }
        case 'error':
          live.error = ev.error
          break
        case 'done':
          if (ev.message) Object.assign(live, ev.message)
          break
      }
    })
  } catch (e) {
    if ((e as Error).name === 'AbortError') live.error ||= t('assistant.stopped')
    else live.error = errorMessage(e)
    if (!current.value?.messages.includes(live)) current.value?.messages.push(live)
  } finally {
    live.tool_calls.forEach((c) => (c.pending = false))
    streaming.value = false
    controller = null
    await loadSessions()
    const s = sessions.value.find((x) => x.id === current.value?.id)
    if (s && current.value) current.value.title = s.title
  }
}

async function readEvents(resp: Response, fn: (ev: ChatEvent) => void) {
  const reader = resp.body!.pipeThrough(new TextDecoderStream()).getReader()
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf += value
    let i: number
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const block = buf.slice(0, i)
      buf = buf.slice(i + 2)
      const data = block
        .split('\n')
        .filter((l) => l.startsWith('data:'))
        .map((l) => l.slice(5).replace(/^ /, ''))
        .join('\n')
      if (data) fn(JSON.parse(data) as ChatEvent)
    }
  }
}
