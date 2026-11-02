import { ref } from 'vue'

const BASE = ''

/** Number of API requests currently in flight (drives the top progress bar). */
export const pendingRequests = ref(0)

export async function api<T = any>(path: string, options?: RequestInit): Promise<T> {
  pendingRequests.value++
  try {
    const resp = await fetch(BASE + path, options)
    if (!resp.ok) throw new Error(`API ${resp.status}: ${resp.statusText}`)
    return await resp.json()
  } finally {
    pendingRequests.value--
  }
}

export function apiPost<T = any>(path: string, body?: any): Promise<T> {
  return api(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
}

export function apiDelete<T = any>(path: string): Promise<T> {
  return api(path, { method: 'DELETE' })
}

export function apiPut<T = any>(path: string, body?: any): Promise<T> {
  return api(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
}
