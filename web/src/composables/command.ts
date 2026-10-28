import { api } from '@/api'
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

export const commandStatusText: Record<string, string> = {
  pending: '等待 agent 领取',
  sent: 'agent 执行中',
  done: '完成',
  failed: '失败',
  expired: '已过期（agent 未在期限内响应）',
}
