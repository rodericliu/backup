import { api } from '@/api'
import { keyed } from '@/i18n'
import type { Command } from '@/types'

const finished = new Set(['done', 'failed', 'expired'])

// waitCommand polls a command sent to an agent until it finishes or timeout
// elapses, and returns its last state.
export async function waitCommand(id: number, timeoutMs = 60_000): Promise<Command> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const c = await api.get<Command>(`/commands/${id}`)
    if (finished.has(c.status) || Date.now() > deadline) return c
    await new Promise((r) => setTimeout(r, 1000))
  }
}

export const commandStatusText = keyed('commandStatus')
