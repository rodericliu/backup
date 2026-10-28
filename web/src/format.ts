// Formatting helpers. Times are shown in the browser's time zone (PRD 9).

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

// ago describes how long ago s was, for example "3 分钟前".
export function ago(s?: string | null, now = Date.now()): string {
  if (!s) return '-'
  const sec = Math.round((now - new Date(s).getTime()) / 1000)
  if (sec < 60) return `${Math.max(sec, 0)} 秒前`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  return `${Math.floor(sec / 86400)} 天前`
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

export const statusText: Record<string, string> = {
  pending: '排队中',
  running: '运行中',
  success: '成功',
  warning: '警告',
  failed: '失败',
  skipped: '跳过',
  canceled: '已取消',
}

export const statusTag: Record<string, 'success' | 'warning' | 'danger' | 'info' | 'primary'> = {
  pending: 'info',
  running: 'primary',
  success: 'success',
  warning: 'warning',
  failed: 'danger',
  skipped: 'info',
  canceled: 'info',
}

export const typeText: Record<string, string> = { full: '全量', incr: '增量' }

export const triggerText: Record<string, string> = { schedule: '计划', manual: '手动', retry: '重试' }

export const hostStatusText: Record<string, string> = {
  online: '在线',
  offline: '离线',
  never: '未连接',
  revoked: '已吊销',
}

export const hostStatusTag: Record<string, 'success' | 'danger' | 'info' | 'warning'> = {
  online: 'success',
  offline: 'danger',
  never: 'warning',
  revoked: 'info',
}

// Failure reason codes (PRD 6.4.5).
export const errorCodeText: Record<string, string> = {
  CONTAINER_NOT_RUNNING: '容器未运行',
  REPLICATION_BROKEN: '复制中断',
  REPLICATION_LAG: '复制延迟超阈值',
  DISK_INSUFFICIENT: '磁盘空间不足',
  MYSQL_AUTH_FAILED: 'MySQL 认证失败',
  XTRABACKUP_ERROR: 'xtrabackup 失败',
  COMPRESS_ERROR: '压缩或写文件失败',
  TIMEOUT: '超时',
  NO_BASE: '无可用基准',
  INTERRUPTED: '中断',
  CANCELED: '已取消',
  SUPERSEDED: '被新任务覆盖',
}
