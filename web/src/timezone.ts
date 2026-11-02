// Times are shown in the time zone configured in server.yaml, which matches
// the agents', so that they read the same as the agents' schedules
// regardless of where the browser is.

import { ref } from 'vue'
import { api } from './api'

// zone is the IANA time zone used to show times; undefined uses the browser's.
export const zone = ref<string>()

let loaded = false

// loadZone fetches the display time zone once.
export async function loadZone() {
  if (loaded) return
  try {
    const { timezone } = await api.get<{ timezone: string }>('/info')
    zone.value = valid(timezone) ? timezone : undefined
    loaded = true
  } catch {
    // Keep the browser's time zone and retry on the next navigation.
  }
}

function valid(tz: string): boolean {
  if (!tz) return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz })
    return true
  } catch {
    return false
  }
}

const formatters = new Map<string, Intl.DateTimeFormat>()

function formatter(): Intl.DateTimeFormat {
  const key = zone.value ?? ''
  let f = formatters.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: zone.value,
      hourCycle: 'h23',
      year: 'numeric',
      month: 'numeric',
      day: 'numeric',
      hour: 'numeric',
      minute: 'numeric',
      second: 'numeric',
    })
    formatters.set(key, f)
  }
  return f
}

export interface WallClock {
  year: number
  month: number // 1-12
  day: number
  hour: number
  minute: number
  second: number
}

// wallClock returns the date and time of t in the display time zone.
export function wallClock(t: Date | number): WallClock {
  const p: Record<string, number> = {}
  for (const { type, value } of formatter().formatToParts(t)) {
    if (type !== 'literal') p[type] = Number(value)
  }
  return { year: p.year, month: p.month, day: p.day, hour: p.hour % 24, minute: p.minute, second: p.second }
}

// toLocal returns a Date whose browser-local fields show t in the display
// time zone, for components that only work in the browser's time zone (the
// date pickers and the chart time axis).
export function toLocal(t: Date | number): Date {
  const ms = typeof t === 'number' ? t : t.getTime()
  const w = wallClock(ms)
  const d = new Date(0)
  d.setFullYear(w.year, w.month - 1, w.day)
  d.setHours(w.hour, w.minute, w.second, ((ms % 1000) + 1000) % 1000)
  return d
}

// fromLocal is the inverse of toLocal: it reads the browser-local fields of d
// as a time in the display time zone.
export function fromLocal(d: Date): Date {
  const guess = Date.UTC(d.getFullYear(), d.getMonth(), d.getDate(), d.getHours(), d.getMinutes(), d.getSeconds(), d.getMilliseconds())
  let t = guess - offset(guess)
  t = guess - offset(t)
  return new Date(t)
}

// offset is the UTC offset of the display time zone at t, in milliseconds.
function offset(t: number): number {
  const w = wallClock(t)
  const ms = ((t % 1000) + 1000) % 1000
  return Date.UTC(w.year, w.month - 1, w.day, w.hour, w.minute, w.second, ms) - t
}

// startOfToday returns midnight today in the display time zone.
export function startOfToday(): Date {
  const w = wallClock(Date.now())
  return fromLocal(new Date(w.year, w.month - 1, w.day))
}
