import { onBeforeUnmount } from 'vue'
import type { Host, Job, Progress } from '@/types'

// Server-sent events of /api/v1/events/stream (PRD D-5). One EventSource is
// shared by all components and closed when the last one unsubscribes.

export interface JobUpdate {
  job: Job
  progress?: Progress
}

type Handlers = {
  host: (h: Host) => void
  job: (u: JobUpdate) => void
}

const handlers: { [K in keyof Handlers]: Set<Handlers[K]> } = { host: new Set(), job: new Set() }
let source: EventSource | null = null

function connect() {
  if (source) return
  source = new EventSource('/api/v1/events/stream')
  for (const type of Object.keys(handlers) as (keyof Handlers)[]) {
    source.addEventListener(type, (e) => {
      const data = JSON.parse((e as MessageEvent).data)
      handlers[type].forEach((fn) => (fn as (d: unknown) => void)(data))
    })
  }
}

function disconnectIfIdle() {
  if (source && handlers.host.size === 0 && handlers.job.size === 0) {
    source.close()
    source = null
  }
}

// useEvents subscribes for the lifetime of the calling component.
export function useEvents<K extends keyof Handlers>(type: K, fn: Handlers[K]) {
  handlers[type].add(fn)
  connect()
  onBeforeUnmount(() => {
    handlers[type].delete(fn)
    disconnectIfIdle()
  })
}
