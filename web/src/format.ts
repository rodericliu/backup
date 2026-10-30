// Formatting helpers. Times are shown in the browser's time zone (PRD 9).

import { keyed, t } from '@/i18n'

const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function bytes(n?: number | null): string {
  if (n === undefined || n === null) return '-'
  let v = n
  let i = 0
  while (Math.abs(v) >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : v < 10 ? 2 : 1)} ${units[i]}`
}

export function rate(bps?: number | null): string {
  return bps ? bytes(bps) + '/s' : '-'
}

const pad = (n: number) => String(n).padStart(2, '0')

export function datetime(s?: string | null): string {
  if (!s) return '-'
  const d = new Date(s)
  if (isNaN(d.getTime()) || d.getFullYear() < 2) return '-'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

export function shortTime(s?: string | null): string {
  if (!s) return '-'
  const d = new Date(s)
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// ago describes how long ago s was, for example "3 minutes ago".
export function ago(s?: string | null, now = Date.now()): string {
  if (!s) return '-'
  const sec = Math.round((now - new Date(s).getTime()) / 1000)
  if (sec < 60) return t('ago.seconds', { n: Math.max(sec, 0) })
  if (sec < 3600) return t('ago.minutes', { n: Math.floor(sec / 60) })
  if (sec < 86400) return t('ago.hours', { n: Math.floor(sec / 3600) })
  return t('ago.days', { n: Math.floor(sec / 86400) })
}

// parseDuration converts a Go duration string to seconds.
export function parseDuration(s?: string | null): number {
  if (!s) return 0
  let total = 0
  const re = /(-?[\d.]+)(h|ms|m|s|us|µs|ns)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(s))) {
    const v = parseFloat(m[1])
    total += { h: 3600, m: 60, s: 1, ms: 1e-3, us: 1e-6, µs: 1e-6, ns: 1e-9 }[m[2]]! * v
  }
  return total
}

// duration formats seconds as "1h 2m" or "45s".
export function duration(sec?: number | null): string {
  if (sec === undefined || sec === null || !isFinite(sec)) return '-'
  sec = Math.round(sec)
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h) return m ? `${h}h ${m}m` : `${h}h`
  if (m) return s ? `${m}m ${s}s` : `${m}m`
  return `${s}s`
}

// goDuration formats a Go duration string compactly ("4h0m0s" → "4h").
export function goDuration(s?: string | null): string {
  return s ? duration(parseDuration(s)) : '-'
}

export function jobDuration(started?: string, finished?: string): string {
  if (!started || !finished) return '-'
  return duration((new Date(finished).getTime() - new Date(started).getTime()) / 1000)
}

export const statusText = keyed('status')

export const statusTag: Record<string, 'success' | 'warning' | 'danger' | 'info' | 'primary'> = {
  pending: 'info',
  running: 'primary',
  success: 'success',
  warning: 'warning',
  failed: 'danger',
  skipped: 'info',
  canceled: 'info',
}

export const typeText = keyed('jobType')

export const triggerText = keyed('trigger')

export const hostStatusText = keyed('hostStatus')

export const chainStatusText = keyed('chainStatus')

export const hostStatusTag: Record<string, 'success' | 'danger' | 'info' | 'warning'> = {
  online: 'success',
  offline: 'danger',
  never: 'warning',
  revoked: 'info',
}

// Failure reason codes (PRD 6.4.5).
export const errorCodeText = keyed('errorCode')
