import { ref } from 'vue'
import { api, ApiError } from './api'
import type { User } from './types'

// user is the signed-in user, or null.
export const user = ref<User | null>(null)

let loaded = false

// loadUser fetches the current user once; it returns null when signed out.
export async function loadUser(force = false): Promise<User | null> {
  if (loaded && !force) return user.value
  try {
    user.value = await api.get<User>('/auth/me')
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) throw e
    user.value = null
  }
  loaded = true
  return user.value
}

export async function login(username: string, password: string) {
  user.value = await api.post<User>('/auth/login', { username, password })
  loaded = true
}

export async function logout() {
  try {
    await api.post('/auth/logout')
  } finally {
    user.value = null
  }
}

export function signedOut() {
  user.value = null
}
