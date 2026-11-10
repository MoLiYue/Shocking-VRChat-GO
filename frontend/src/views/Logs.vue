<script setup lang="ts">
import AppSelect from '@/components/AppSelect.vue'
import { ref, onMounted, onUnmounted, onActivated, onDeactivated, nextTick } from 'vue'
import { api } from '@/api'
import { useI18n } from '@/i18n'

const { t } = useI18n()

interface LogEntry {
  text: string
  level: string
  time: number
}

const logs = ref<LogEntry[]>([])
const autoScroll = ref(true)
const filterLevel = ref('all')
const filterText = ref('')
const logAreaRef = ref<HTMLDivElement | null>(null)
let ws: WebSocket | null = null

const LEVELS = ['all', 'debug', 'info', 'success', 'warning', 'error', 'critical']

// OSC events (moved here from the dashboard): every routed parameter update.
interface OscEvent { time: number; address: string; value: number; channel: string; mode: string }
const oscEvents = ref<OscEvent[]>([])
const showOsc = ref(true)
const OSC_MAX = 200
function shortPath(a: string) { return a.replace('/avatar/parameters/', '') }
function fmtClock(ts: number) {
  const d = new Date(ts * 1000)
  return d.toLocaleTimeString('en-GB', { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0')
}

async function loadInitial() {
  try { oscEvents.value = ((await api('/api/v1/osc_activity')).events || []).slice(0, OSC_MAX) } catch {}
  const data = await api('/api/v1/logs')
  logs.value = data.logs || []
  await nextTick()
  scrollToBottom()
}

let wanted = false
let reconnectTimer = 0
function connectWs() {
  if (ws || !wanted) return
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const sock = new WebSocket(`${proto}//${location.host}/ws/live`)
  ws = sock
  sock.onopen = () => { sock.send(JSON.stringify({ subscribe: ['log', 'osc'] })) }
  sock.onmessage = (ev) => {
    if (ws !== sock) return // late message from a replaced socket
    try {
      const msg = JSON.parse(ev.data)
      if (msg.topic === 'osc' && msg.event) {
        oscEvents.value.unshift(msg.event)
        if (oscEvents.value.length > OSC_MAX) oscEvents.value.length = OSC_MAX
      } else if (msg.topic === 'log') {
        logs.value.push({ text: msg.text, level: msg.level, time: msg.time })
        if (logs.value.length > 2000) logs.value = logs.value.slice(-1500)
        if (autoScroll.value) {
          nextTick(scrollToBottom)
        }
      }
    } catch {}
  }
  sock.onclose = () => {
    if (ws !== sock) return // replaced: must not drop the new socket
    ws = null
    if (wanted) reconnectTimer = window.setTimeout(connectWs, 2000)
  }
  sock.onerror = () => { sock.close() }
}

function scrollToBottom() {
  const el = logAreaRef.value
  if (el) el.scrollTop = el.scrollHeight
}

function clearLogs() {
  logs.value = []
}

function filteredLogs(): LogEntry[] {
  let result = logs.value
  if (filterLevel.value !== 'all') {
    result = result.filter(l => l.level === filterLevel.value)
  }
  if (filterText.value) {
    const q = filterText.value.toLowerCase()
    result = result.filter(l => l.text.toLowerCase().includes(q))
  }
  return result
}

function levelClass(level: string): string {
  return 'log-' + level
}

function stopWs() {
  wanted = false
  clearTimeout(reconnectTimer)
  if (ws) { const sock = ws; ws = null; sock.close() }
}
onMounted(loadInitial)
// Re-sync on return: entries logged while hidden were not streamed.
onActivated(() => { if (!wanted) { wanted = true; if (logs.value.length) loadInitial(); connectWs() } })
onDeactivated(stopWs)
onUnmounted(stopWs)
</script>

<template>
  <div class="logs-page">
    <div class="logs-header">
      <h1 class="gradient-text page-title">{{ t('logs.title') }}</h1>
      <div class="logs-controls">
        <AppSelect v-model="filterLevel" class="filter-select" :options="[...(LEVELS).map((lv) => ({ value: lv, label: lv.toUpperCase() }))]" />
        <input type="text" v-model="filterText" :placeholder="t('logs.filter')" class="filter-input">
        <label class="auto-scroll-check">
          <input type="checkbox" class="switch" role="switch" v-model="autoScroll">
          <span>{{ t('logs.autoScroll') }}</span>
        </label>
        <button class="btn btn-ghost btn-sm" @click="clearLogs">{{ t('logs.clear') }}</button>
      </div>
    </div>
    <section class="osc-panel">
      <button class="osc-head" :aria-expanded="showOsc" @click="showOsc = !showOsc">
        {{ showOsc ? '▾' : '▸' }} {{ t('dashboard.oscEvents') }} <span class="osc-count">{{ oscEvents.length }}</span>
      </button>
      <div v-if="showOsc" class="osc-feed">
        <div class="osc-row" v-for="(e, i) in oscEvents" :key="i">
          <span class="osc-time">{{ fmtClock(e.time) }}</span>
          <span class="ch-tag sm" :class="'ch-' + e.channel.toLowerCase()">{{ e.channel }}</span>
          <span class="osc-path" :title="e.address">{{ shortPath(e.address) }}</span>
          <span class="osc-mode">{{ e.mode }}</span>
          <span class="osc-val">{{ e.value }}</span>
        </div>
        <div v-if="!oscEvents.length" class="log-empty">{{ t('dashboard.noEvents') }}</div>
      </div>
    </section>
    <div ref="logAreaRef" class="log-area">
      <div
        v-for="(entry, i) in filteredLogs()"
        :key="i"
        class="log-line"
        :class="levelClass(entry.level)"
      >{{ entry.text }}</div>
      <div v-if="!filteredLogs().length" class="log-empty">{{ t('logs.empty') }}</div>
    </div>
  </div>
</template>

<style scoped>
.logs-page { display: flex; flex-direction: column; height: calc(100vh - 4rem); }
.logs-header { display: flex; align-items: center; gap: var(--sp-4); margin-bottom: var(--sp-3); flex-wrap: wrap; }
.logs-header h1 { margin: 0; white-space: nowrap; }
.logs-controls { display: flex; align-items: center; gap: var(--sp-2); flex: 1; flex-wrap: wrap; }
.filter-select { width: 120px; min-width: 0; }
.filter-input { flex: 1; min-width: 120px; max-width: 250px; font-size: var(--text-xs); padding: var(--sp-1) var(--sp-2); font-family: var(--font-mono); }
.auto-scroll-check { display: flex; align-items: center; gap: var(--sp-1); font-size: var(--text-xs); color: var(--text-muted); cursor: pointer; white-space: nowrap; }
.auto-scroll-check input { accent-color: var(--accent); }

.log-area {
  flex: 1;
  overflow-y: auto;
  background: var(--bg-inset);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  padding: var(--sp-3);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  line-height: 1.6;
}

.log-line {
  white-space: pre-wrap;
  word-break: break-all;
  padding: 1px 0;
}

/* Level colors matching loguru */
.log-debug { color: var(--text-muted); }
.log-info { color: var(--text-label); }
.log-success { color: var(--success); }
.log-warning { color: var(--warning); }
.log-error { color: var(--danger); }
.log-critical { color: var(--danger); font-weight: 700; background: var(--danger-surface); }

.osc-panel { margin-bottom: var(--sp-3); }
.osc-head { background: none; border: none; padding: 0 0 var(--sp-1); color: var(--text-label); font-size: var(--text-sm); font-weight: 600; cursor: pointer; }
.osc-count { font-weight: 400; color: var(--text-muted); font-size: var(--text-xs); margin-left: 4px; }
.osc-feed { max-height: 200px; overflow-y: auto; background: var(--bg-inset); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--sp-1) var(--sp-2); }
.osc-row { display: flex; gap: var(--sp-3); align-items: center; padding: 2px 0; font-size: var(--text-xs); font-family: var(--font-mono); }
.osc-time { color: var(--text-muted); font-variant-numeric: tabular-nums; }
.osc-path { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); }
.osc-mode { color: var(--text-muted); }
.osc-val { min-width: 56px; text-align: right; color: var(--text); font-variant-numeric: tabular-nums; }
</style>
