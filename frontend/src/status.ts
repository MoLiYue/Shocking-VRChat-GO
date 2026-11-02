// Shared device/backend status, polled once for the whole app.
import { reactive } from 'vue'

export const appStatus = reactive({
  /** backend answered the last poll */
  reachable: true,
  devices: 0,
  engineRunning: false,
})

let timer: number | null = null

async function poll() {
  try {
    const r = await fetch('/api/v1/status', { signal: AbortSignal.timeout(2500) })
    if (!r.ok) throw new Error(String(r.status))
    const d = await r.json()
    appStatus.reachable = true
    appStatus.devices = (d.devices || []).length
    appStatus.engineRunning = !!d.engine_running
  } catch {
    appStatus.reachable = false
  }
}

export function startStatusPolling(intervalMs = 3000) {
  if (timer !== null) return
  poll()
  timer = window.setInterval(poll, intervalMs)
}
