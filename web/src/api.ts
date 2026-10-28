// Client of the web API (/api/v1). Requests use the session cookie.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message)
  }
}

type Query = Record<string, string | number | boolean | string[] | undefined | null>

// onUnauthorized is called when a request fails with 401 (session expired).
let onUnauthorized: () => void = () => {}

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export function queryString(q?: Query): string {
  if (!q) return ''
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null || v === '') continue
    if (Array.isArray(v)) v.forEach((x) => p.append(k, x))
    else p.set(k, String(v))
  }
  const s = p.toString()
  return s ? '?' + s : ''
}

async function request<T>(method: string, path: string, body?: unknown, q?: Query): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin', headers: {} }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  const resp = await fetch('/api/v1' + path + queryString(q), init)
  if (resp.status === 204) return undefined as T
  const text = await resp.text()
  if (!resp.ok) {
    let code = 'HTTP_' + resp.status
    let message = text || resp.statusText
    try {
      const e = JSON.parse(text).error
      if (e) ({ code, message } = e)
    } catch {
      // not JSON
    }
    if (resp.status === 401 && !path.startsWith('/auth/login')) onUnauthorized()
    throw new ApiError(resp.status, code, message)
  }
  const type = resp.headers.get('Content-Type') ?? ''
  return (type.includes('application/json') ? JSON.parse(text) : text) as T
}

export const api = {
  get: <T>(path: string, q?: Query) => request<T>('GET', path, undefined, q),
  post: <T>(path: string, body?: unknown, q?: Query) => request<T>('POST', path, body ?? {}, q),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string, q?: Query) => request<T>('DELETE', path, undefined, q),
}

// errorMessage returns a message suitable for ElMessage.
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}
