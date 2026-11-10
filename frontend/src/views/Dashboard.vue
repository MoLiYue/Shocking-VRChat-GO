<script setup lang="ts">
import { themeColor } from '@/canvasFont'
import { ref, reactive, computed, onMounted, onUnmounted, onActivated, onDeactivated } from 'vue'
import { api } from '@/api'
import QrCode from '@/components/QrCode.vue'
import { Radio, Clock, Gauge, Copy, QrCode as QrIcon, ChevronUp } from 'lucide-vue-next'
import { toast } from '@/dialog'
import { useI18n } from '@/i18n'

const { t } = useI18n()

// --- State ---
const connected = ref(false)
const deviceCount = ref(0)
const strength = ref({ A: 0, B: 0 })
const limit = ref({ A: 100, B: 100 })
// Base strength limit (strength page) and the current overlimit boost on top.
const baseLimit = ref({ A: 0, B: 0 })
const boost = ref({ A: 0, B: 0 })

interface UsedSource { kind: 'param' | 'overlimit'; channel: string; mode?: string; rule?: string; operator?: string; value?: number; enabled: boolean }
interface UsedParam { path: string; sources: UsedSource[]; wildcard: boolean; last_value?: unknown; last_seen_ago?: number }
const usedParams = ref<UsedParam[]>([])
// Parameters with no enabled use at all are hidden; partially enabled ones
// show their enabled and disabled uses separately.
const activeUsedParams = computed(() => usedParams.value.filter((p) => p.sources.some((s) => s.enabled)))
const oscStatus = ref('')
const lastTrigger = ref('-')
// Latest value per parameter straight from the live OSC feed (the value
// the program actually uses); the 1 s poll covers params not routed to a
// mode (e.g. only used by overlimit rules).
const liveValues = reactive<Record<string, { v: unknown; at: number }>>({})
const nowMs = ref(Date.now())
const canvasARef = ref<HTMLCanvasElement | null>(null)
const canvasBRef = ref<HTMLCanvasElement | null>(null)
// Function ref: string refs inside v-for would collect into arrays.
function setCanvas(ch: 'A' | 'B', el: unknown) {
  ;(ch === 'A' ? canvasARef : canvasBRef).value = (el as HTMLCanvasElement) || null
}
const qrContent = ref('')

// V4 QR
const qrContentV4 = ref('')
const v4Enabled = ref(true)
const qrMode = ref<'v3' | 'v4'>('v4')  // Default to V4
// Once a device is connected the QR is no longer needed: keep it folded
// unless the user explicitly opens it.
const qrExpanded = ref(false)
const configLimit = ref<{ A: number | string; B: number | string }>({ A: '-', B: '-' })
// OSC messages per second (rolling 3s window, from the live feed)
const oscStamps: number[] = []
const oscRate = ref(0)
let intervals: number[] = []
let dashWs: WebSocket | null = null

// Wave animation state (same approach as WaveTest: pending queue + rAF metering)
const DISPLAY_SLOTS = 200
const SLOT_MS = 25
let pendingA: {s: number; f: number}[] = []
let pendingB: {s: number; f: number}[] = []
let bufferA: {s: number; f: number}[] = []
let bufferB: {s: number; f: number}[] = []
let debtA = 0
let debtB = 0
let animId = 0
let lastFrame = 0

// --- WebSocket-based real-time data ---
// Pages are kept alive; live work only runs while this page is shown.
let liveWanted = false
let reconnectTimer = 0
function connectLiveWs() {
  if (dashWs || !liveWanted) return
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const wsUrl = `${proto}//${location.host}/ws/live`
  const ws = new WebSocket(wsUrl)
  dashWs = ws
  ws.onopen = () => {
    ws.send(JSON.stringify({ subscribe: ['wave_A', 'wave_B', 'osc', 'status'] }))
  }
  ws.onmessage = (ev) => {
    if (dashWs !== ws) return // a closed socket's late messages
    try {
      const msg = JSON.parse(ev.data)
      if (msg.topic === 'wave_A' && msg.samples) {
        pendingA.push(...msg.samples)
      } else if (msg.topic === 'wave_B' && msg.samples) {
        pendingB.push(...msg.samples)
      } else if (msg.topic === 'osc' && msg.event) {
        oscStamps.push(Date.now())
        liveValues[msg.event.address] = { v: msg.event.value, at: Date.now() }
        lastTrigger.value = t('dashboard.justNow')
      } else if (msg.topic === 'status') {
        const devices = msg.devices || []
        deviceCount.value = devices.length
        connected.value = devices.length > 0
        if (devices.length > 0) {
          strength.value = devices[0].strength || { A: 0, B: 0 }
          applyLimits(devices[0])
          limit.value = {
            A: Math.min(devices[0].strength_max?.A || 0, devices[0].strength_limit?.A || 100),
            B: Math.min(devices[0].strength_max?.B || 0, devices[0].strength_limit?.B || 100),
          }
        }
      }
    } catch {}
  }
  ws.onclose = () => {
    // Leaving and re-entering the page replaces the socket before the old
    // one finishes closing; its onclose must not drop the new socket
    // (which led to a duplicate connection and doubled wave samples).
    if (dashWs !== ws) return
    dashWs = null
    if (!liveWanted) return
    // Reconnect after 2s
    reconnectTimer = window.setTimeout(connectLiveWs, 2000)
  }
  ws.onerror = () => { ws.close() }
}

function disconnectLiveWs() {
  liveWanted = false
  clearTimeout(reconnectTimer)
  if (dashWs) { const ws = dashWs; dashWs = null; ws.close() }
}

function applyLimits(d: any) {
  baseLimit.value = { A: d.strength_limit?.A ?? 0, B: d.strength_limit?.B ?? 0 }
  boost.value = { A: d.limit_boost?.A ?? 0, B: d.limit_boost?.B ?? 0 }
}

async function pollUsedParams() {
  try { usedParams.value = (await api('/api/v1/params/used')).params || [] } catch {}
}

// --- Polling (fallback for initial load and status heartbeat) ---
async function pollStatus() {
  try {
    const data = await api('/api/v1/status')
    const devices = data.devices || []
    deviceCount.value = devices.length
    connected.value = devices.length > 0
    if (devices.length > 0) {
      const attr = devices[0].attr
      strength.value = attr.strength
      applyLimits(attr)
      limit.value = {
        A: Math.min(attr.strength_max?.A || 0, attr.strength_limit?.A || 100),
        B: Math.min(attr.strength_max?.B || 0, attr.strength_limit?.B || 100),
      }
    }
    oscStatus.value = data.osc_listening || ''
    try {
      const l = await api('/api/v1/strength_limit')
      configLimit.value = { A: l.channel_a ?? '-', B: l.channel_b ?? '-' }
    } catch {}
    if (data.last_osc_time) {
      const ago = Math.round(Date.now() / 1000 - data.last_osc_time)
      lastTrigger.value = ago < 2 ? t('dashboard.justNow') : `${ago}s`
    } else { lastTrigger.value = t('dashboard.noData') }
  } catch { connected.value = false }
}

// Bars use the channel colour; alpha encodes pulse frequency.
function chRGB(ch: 'A' | 'B'): number[] {
  const hex = themeColor(ch === 'A' ? '--ch-a-color' : '--ch-b-color', '#a78bfa').replace('#', '')
  return [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16))
}

function drawWaveCanvas(canvas: HTMLCanvasElement | null, buffer: {s: number; f: number}[], ch: 'A' | 'B') {
  if (!canvas) return
  const dpr = window.devicePixelRatio || 1
  const W = canvas.clientWidth
  const H = canvas.clientHeight
  if (W < 1 || H < 1) return
  canvas.width = W * dpr
  canvas.height = H * dpr
  const ctx = canvas.getContext('2d')!
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)

  ctx.fillStyle = themeColor('--canvas-bg')
  ctx.fillRect(0, 0, W, H)

  ctx.strokeStyle = themeColor('--canvas-grid')
  ctx.lineWidth = 1
  for (let pct = 25; pct <= 75; pct += 25) {
    const py = H - (pct / 100) * H
    ctx.beginPath(); ctx.moveTo(0, py); ctx.lineTo(W, py); ctx.stroke()
  }

  if (!buffer.length) return
  const rgb = chRGB(ch)

  const slotW = W / DISPLAY_SLOTS
  const startIdx = Math.max(0, buffer.length - DISPLAY_SLOTS)
  const visibleCount = buffer.length - startIdx
  const xBase = (DISPLAY_SLOTS - visibleCount) * slotW

  for (let i = 0; i < visibleCount; i++) {
    const sample = buffer[startIdx + i]
    if (sample.s <= 0) continue

    const dutyCycle = Math.max(0.05, 1 - (sample.f - 10) / (240 - 10))
    const barW = slotW * dutyCycle
    const barH = (sample.s / 100) * H
    const x = xBase + i * slotW + (slotW - barW) / 2

    // High frequency (short interval) = bright, low frequency = dimmer
    const freqT = Math.max(0, Math.min(1, 1 - (sample.f - 10) / 230))
    const [r, g, b] = rgb
    ctx.fillStyle = `rgba(${r},${g},${b},${0.45 + 0.45 * freqT})`
    ctx.fillRect(x, H - barH, Math.max(barW, 0.5), barH)
  }
}

function meterAndDraw(now: number) {
  if (lastFrame > 0) {
    const dt = now - lastFrame
    const samplesOwed = dt / SLOT_MS
    // Channel A
    debtA += samplesOwed
    if (pendingA.length > 0) {
      const n = Math.min(Math.floor(debtA), pendingA.length)
      if (n > 0) { bufferA.push(...pendingA.splice(0, n)); debtA -= n }
    } else {
      // No incoming data: push empty slots to scroll out old waveform
      const n = Math.floor(debtA)
      if (n > 0) {
        for (let i = 0; i < n; i++) bufferA.push({ s: 0, f: 0 })
        debtA -= n
      }
    }
    if (bufferA.length > DISPLAY_SLOTS * 2) bufferA = bufferA.slice(-DISPLAY_SLOTS * 2)
    // Channel B
    debtB += samplesOwed
    if (pendingB.length > 0) {
      const n = Math.min(Math.floor(debtB), pendingB.length)
      if (n > 0) { bufferB.push(...pendingB.splice(0, n)); debtB -= n }
    } else {
      const n = Math.floor(debtB)
      if (n > 0) {
        for (let i = 0; i < n; i++) bufferB.push({ s: 0, f: 0 })
        debtB -= n
      }
    }
    if (bufferB.length > DISPLAY_SLOTS * 2) bufferB = bufferB.slice(-DISPLAY_SLOTS * 2)
  } else {
    // First frame: dump all
    if (pendingA.length) { bufferA.push(...pendingA.splice(0)); debtA = 0 }
    if (pendingB.length) { bufferB.push(...pendingB.splice(0)); debtB = 0 }
    if (bufferA.length > DISPLAY_SLOTS * 2) bufferA = bufferA.slice(-DISPLAY_SLOTS * 2)
    if (bufferB.length > DISPLAY_SLOTS * 2) bufferB = bufferB.slice(-DISPLAY_SLOTS * 2)
  }
  lastFrame = now
  drawWaveCanvas(canvasARef.value, bufferA, 'A')
  drawWaveCanvas(canvasBRef.value, bufferB, 'B')
  animId = requestAnimationFrame(meterAndDraw)
}

function startWaveAnim() {
  if (!animId) animId = requestAnimationFrame(meterAndDraw)
}
function stopWaveAnim() {
  if (animId) { cancelAnimationFrame(animId); animId = 0 }
  lastFrame = 0
}


// QR
async function loadQr() {
  try {
    const data = await api('/api/v1/qr_payload')
    qrContent.value = data.content || ''
  } catch {}
  try {
    const data = await api('/api/v1/qr_payload_v4')
    qrContentV4.value = data.content || ''
    v4Enabled.value = data.enabled !== false
  } catch {}
}

function updateOscRate() {
  const cutoff = Date.now() - 3000
  while (oscStamps.length && oscStamps[0] < cutoff) oscStamps.shift()
  oscRate.value = Math.round(oscStamps.length / 3)
}

async function copyQr() {
  const text = qrMode.value === 'v4' ? qrContentV4.value : qrContent.value
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Clipboard API can be unavailable on plain-http LAN origins
    const ta = document.createElement('textarea')
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'
    document.body.appendChild(ta); ta.select(); document.execCommand('copy'); ta.remove()
  }
  toast(t('dashboard.copied'), 'success')
}

// Helpers
// Output strength split into the part within the base limit and the part
// above it (allowed only by an active overlimit boost).
function basePart(ch: 'A' | 'B') { return Math.min(strength.value[ch], baseLimit.value[ch] || strength.value[ch]) }
function overPart(ch: 'A' | 'B') { return Math.max(0, strength.value[ch] - basePart(ch)) }
function pct(v: number) { return Math.min(v / 200 * 100, 100) }

function modeLabel(mode: string) {
  const key = 'params.mode' + mode.charAt(0).toUpperCase() + mode.slice(1)
  const tr = t(key)
  return tr !== key ? tr : mode
}
function sourceLabel(s: UsedSource) {
  if (s.kind === 'param') return `${s.channel} · ${modeLabel(s.mode || '')}`
  return `${t('dashboard.usedOverlimit')} · ${s.rule || '-'}`
}
function currentValue(p: UsedParam): unknown {
  const l = liveValues[p.path]
  if (l && (p.last_seen_ago == null || Date.now() - l.at < p.last_seen_ago * 1000 + 1000)) return l.v
  return p.last_value
}
function isFresh(p: UsedParam) {
  const l = liveValues[p.path]
  return (l && nowMs.value - l.at < 2000) || (p.last_seen_ago != null && p.last_seen_ago < 2)
}
function fmtValue(v: unknown) {
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : v.toFixed(3)
  return String(v)
}
function fmtAgo(sec?: number) {
  if (sec == null) return ''
  if (sec < 2) return t('dashboard.justNow')
  if (sec < 60) return `${Math.round(sec)}s`
  if (sec < 3600) return `${Math.round(sec / 60)}min`
  return `${Math.round(sec / 3600)}h`
}
function shortPath(addr: string) { return addr.replace('/avatar/parameters/', '') }

function startLive() {
  if (liveWanted) return
  liveWanted = true
  pollStatus()
  pollUsedParams()
  connectLiveWs()
  startWaveAnim()
  intervals.push(window.setInterval(pollStatus, 5000))
  intervals.push(window.setInterval(updateOscRate, 1000))
  intervals.push(window.setInterval(pollUsedParams, 1000))
  intervals.push(window.setInterval(() => { nowMs.value = Date.now() }, 500))
}
function stopLive() {
  intervals.forEach(clearInterval)
  intervals.length = 0
  disconnectLiveWs()
  stopWaveAnim()
}

onMounted(loadQr)
onActivated(startLive)
onDeactivated(stopLive)
onUnmounted(stopLive)
</script>

<template>
  <div class="dashboard">
    <!-- Stats row: OSC input, last trigger, configured limits (device state lives in the sidebar + output card) -->
    <div class="stats-row">
      <div class="stat-card">
        <Radio class="stat-icon" :size="20" :stroke-width="1.75" />
        <div>
          <div class="stat-val">{{ oscStatus || '-' }}</div>
          <div class="stat-sub">OSC · {{ t('dashboard.oscRate', { n: oscRate }) }}</div>
        </div>
      </div>
      <div class="stat-card">
        <Clock class="stat-icon" :size="20" :stroke-width="1.75" />
        <div><div class="stat-val">{{ lastTrigger }}</div><div class="stat-sub">{{ t('dashboard.lastTrigger') }}</div></div>
      </div>
      <router-link to="/strength" class="stat-card stat-link">
        <Gauge class="stat-icon" :size="20" :stroke-width="1.75" />
        <div>
          <div class="stat-val">
            <span class="ch-a-text">A {{ configLimit.A }}</span><span class="stat-sep">·</span><span class="ch-b-text">B {{ configLimit.B }}</span>
          </div>
          <div class="stat-sub">{{ t('dashboard.limits') }}</div>
        </div>
      </router-link>
    </div>

    <div class="grid-2">
      <!-- Left column -->
      <div class="col">
        <!-- Per-channel output: strength + live waveform together -->
        <section class="card" data-tour="dashboard-status">
          <div class="card-head">
            <h2>{{ t('dashboard.channelOutput') }}</h2>
            <span class="conn-pill" :class="{ on: connected }">
              <span class="dot" />{{ connected ? t('dashboard.deviceCount', { count: deviceCount }) : t('dashboard.disconnected') }}
            </span>
          </div>
          <div class="out-row" v-for="ch in (['A', 'B'] as const)" :key="ch" :class="'ch-' + ch.toLowerCase()">
            <div class="out-head">
              <span class="ch-tag">{{ ch }}</span>
              <div class="bar-track" role="img"
                :aria-label="t('dashboard.strengthAria', { base: basePart(ch), over: overPart(ch) })">
                <div class="bar-fill" :style="{ width: pct(basePart(ch)) + '%' }"></div>
                <div class="bar-over" :style="{ left: pct(basePart(ch)) + '%', width: pct(overPart(ch)) + '%' }"></div>
                <div v-if="boost[ch] > 0 && baseLimit[ch] > 0" class="bar-mark" :style="{ left: pct(baseLimit[ch]) + '%' }"
                  :title="t('dashboard.baseLimitMark', { n: baseLimit[ch] })"></div>
              </div>
              <span class="out-val">
                <strong>{{ strength[ch] }}</strong>
                <span v-if="overPart(ch) > 0" class="over-val" :title="t('dashboard.overPart')">(+{{ overPart(ch) }})</span>
                / 200
              </span>
            </div>
            <canvas :ref="(el) => setCanvas(ch, el)" class="wave-canvas"></canvas>
          </div>
          <div class="legend">
            <span><i class="sw base" />{{ t('dashboard.legendBase') }}</span>
            <span><i class="sw over" />{{ t('dashboard.legendOver') }}</span>
          </div>
        </section>


        <!-- Parameters the configuration listens to -->
        <section class="card">
          <div class="card-head">
            <h2>{{ t('dashboard.usedTitle') }}</h2>
            <router-link to="/params" class="head-link">{{ t('dashboard.usedManage') }}</router-link>
          </div>
          <div class="used-list">
            <div v-for="p in activeUsedParams" :key="p.path" class="used-row">
              <span class="live-dot" :class="{ on: isFresh(p) }" />
              <span class="used-path" :title="p.path">{{ shortPath(p.path) }}</span>
              <span class="used-val" :class="{ none: currentValue(p) === undefined }">{{ currentValue(p) === undefined ? '—' : fmtValue(currentValue(p)) }}</span>
              <span class="used-tags">
                <span v-for="(s, i) in p.sources.filter((x) => x.enabled)" :key="'on' + i" class="used-tag" :class="s.kind"
                  :title="s.kind === 'overlimit' ? `${s.operator} ${s.value ?? ''}` : ''">{{ sourceLabel(s) }}</span>
                <template v-if="p.sources.some((x) => !x.enabled)">
                  <span class="used-off-label">{{ t('dashboard.usedDisabled') }}</span>
                  <span v-for="(s, i) in p.sources.filter((x) => !x.enabled)" :key="'off' + i" class="used-tag off" :class="s.kind"
                    :title="s.kind === 'overlimit' ? `${s.operator} ${s.value ?? ''}` : ''">{{ sourceLabel(s) }}</span>
                </template>
              </span>
              <span class="used-ago">{{ p.last_seen_ago != null ? fmtAgo(p.last_seen_ago) : (p.wildcard ? '' : t('dashboard.usedNever')) }}</span>
            </div>
            <div v-if="!activeUsedParams.length" class="empty">{{ t('dashboard.usedNone') }}</div>
          </div>
        </section>
      </div>

      <!-- Right column -->
      <div class="col">
        <section class="card" data-tour="qr-code">
          <div class="card-head">
            <h2>{{ t('dashboard.qrTitle') }}</h2>
            <button v-if="connected && qrExpanded" class="icon-action" :title="t('dashboard.hideQr')" :aria-label="t('dashboard.hideQr')" @click="qrExpanded = false">
              <ChevronUp :size="16" />
            </button>
          </div>

          <div v-if="connected && !qrExpanded" class="qr-folded">
            <p>{{ t('dashboard.qrConnected') }}</p>
            <button class="btn btn-ghost btn-sm" @click="qrExpanded = true"><QrIcon :size="14" />{{ t('dashboard.showQr') }}</button>
          </div>

          <template v-else>
            <div class="qr-tabs">
              <button class="qr-tab" :class="{ active: qrMode === 'v4' }" @click="qrMode = 'v4'" v-if="v4Enabled">V4</button>
              <button class="qr-tab" :class="{ active: qrMode === 'v3' }" @click="qrMode = 'v3'">V3</button>
              <span class="qr-badge" :class="qrMode">{{ qrMode === 'v4' ? t('dashboard.qrV4Badge') : t('dashboard.qrV3Badge') }}</span>
            </div>
            <QrCode :content="qrMode === 'v4' ? qrContentV4 : qrContent" :size="240" />
            <div class="qr-foot">
              <span class="qr-hint">{{ qrMode === 'v4' ? t('dashboard.qrV4Hint') : t('dashboard.qrV3Hint') }}</span>
              <button class="btn btn-ghost btn-sm" :title="qrMode === 'v4' ? qrContentV4 : qrContent" @click="copyQr">
                <Copy :size="13" />{{ t('dashboard.copyLink') }}
              </button>
            </div>
          </template>
        </section>

      </div>
    </div>
  </div>
</template>

<style scoped>
.dashboard { display: flex; flex-direction: column; gap: var(--sp-5); }

.stats-row { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--sp-4); }
.stat-card {
  display: flex; align-items: center; gap: var(--sp-4);
  padding: var(--sp-4) var(--sp-5);
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius-xl);
  color: var(--text);
  text-decoration: none;
  transition: var(--transition-props);
}
.stat-link:hover { border-color: var(--border-hover); background: var(--bg-card-hover); text-decoration: none; }
.stat-icon { color: var(--text-secondary); flex-shrink: 0; }
.stat-val { font-size: var(--text-base); font-weight: 600; font-variant-numeric: tabular-nums; }
.stat-sep { margin: 0 var(--sp-2); color: var(--text-muted); }
.stat-sub { font-size: var(--text-xs); color: var(--text-muted); margin-top: 2px; }

.grid-2 { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: var(--sp-5); align-items: start; }
.col { display: flex; flex-direction: column; gap: var(--sp-4); min-width: 0; }

.card-head { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-3); margin-bottom: var(--sp-4); }
.card-head h2 { margin: 0; }

.conn-pill { display: inline-flex; align-items: center; gap: 6px; font-size: var(--text-xs); color: var(--text-muted); }
.conn-pill .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-muted); }
.conn-pill.on { color: var(--success); }
.conn-pill.on .dot { background: var(--success); box-shadow: 0 0 0 3px var(--success-surface); }

/* Channel output */
.out-row + .out-row { margin-top: var(--sp-4); }
.out-head { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-2); }
.bar-track { flex: 1; height: 6px; background: var(--track); border-radius: var(--radius-full); overflow: hidden; }
.bar-track { position: relative; }
.bar-fill { position: absolute; left: 0; top: 0; height: 100%; border-radius: var(--radius-full); background: var(--ch-color); transition: width 200ms linear; }
.bar-over { position: absolute; top: 0; height: 100%; background: var(--warning); border-radius: 0 var(--radius-full) var(--radius-full) 0; transition: left 200ms linear, width 200ms linear; }
.bar-mark { position: absolute; top: -2px; width: 2px; height: 10px; background: var(--text-muted); transform: translateX(-1px); }
.over-val { color: var(--warning); font-weight: 600; margin-left: 2px; }
.legend { display: flex; gap: var(--sp-4); margin-top: var(--sp-3); font-size: var(--text-xs); color: var(--text-muted); }
.legend span { display: inline-flex; align-items: center; gap: 6px; }
.sw { display: inline-block; width: 12px; height: 6px; border-radius: 3px; }
.sw.base { background: linear-gradient(90deg, var(--ch-a-color) 50%, var(--ch-b-color) 50%); }
.sw.over { background: var(--warning); }

/* Used parameters */
.head-link { font-size: var(--text-xs); color: var(--text-muted); }
.used-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 460px), 1fr)); gap: 2px var(--sp-4); }
.used-row { display: flex; align-items: center; gap: var(--sp-2); padding: 5px var(--sp-2); border-radius: var(--radius-sm); min-width: 0; }
.used-row:hover { background: var(--bg-hover); }
.used-path { flex: 0 1 auto; min-width: 0; max-width: 45%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); }
.used-val { flex-shrink: 0; min-width: 52px; font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text); font-variant-numeric: tabular-nums; }
.used-val.none { color: var(--text-muted); font-weight: 400; }
.live-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--text-muted); opacity: 0.4; flex-shrink: 0; }
.live-dot.on { background: var(--success); opacity: 1; }
.used-tags { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.used-tag { font-size: var(--text-2xs); padding: 1px 6px; border-radius: var(--radius-full); background: var(--bg-tertiary); color: var(--text-secondary); }
.used-tag.overlimit { background: var(--warning-surface, rgba(255, 170, 0, 0.12)); color: var(--warning); }
.used-tag.off { opacity: 0.5; text-decoration: line-through; }
.used-off-label { font-size: var(--text-2xs); color: var(--text-muted); margin-left: var(--sp-1); }
.used-ago { flex-shrink: 0; font-size: var(--text-2xs); color: var(--text-muted); margin-left: auto; }
.out-val { font-size: var(--text-sm); color: var(--text-muted); font-variant-numeric: tabular-nums; min-width: 72px; text-align: right; }
.out-val strong { color: var(--text); }
.wave-canvas { width: 100%; height: 72px; border-radius: var(--radius-md); display: block; }


/* QR */
.qr-tabs { display: flex; align-items: center; gap: var(--sp-2); margin-bottom: var(--sp-2); }
.qr-tab { padding: var(--sp-1) var(--sp-3); border: 1px solid var(--border); border-radius: var(--radius-sm); background: transparent; color: var(--text-muted); cursor: pointer; font-size: var(--text-xs); font-weight: 600; transition: var(--transition-props); }
.qr-tab.active { border-color: var(--accent); color: var(--text); background: var(--bg-active); }
.qr-tab:hover { border-color: var(--border-hover); color: var(--text); }
.qr-badge { margin-left: auto; padding: 2px 8px; border-radius: var(--radius-full); font-size: var(--text-2xs); font-weight: 600; }
.qr-badge.v4 { background: var(--success-surface); color: var(--success); }
.qr-badge.v3 { background: var(--info-surface); color: var(--info); }
.qr-foot { display: flex; flex-direction: column; align-items: flex-start; gap: var(--sp-2); margin-top: var(--sp-1); }
.qr-hint { font-size: var(--text-xs); color: var(--text-muted); }
.qr-folded { display: flex; flex-direction: column; align-items: flex-start; gap: var(--sp-3); color: var(--text-muted); font-size: var(--text-sm); }

@media (max-width: 1000px) {
  .grid-2 { grid-template-columns: 1fr; }
}
@media (max-width: 768px) {
  .stats-row { grid-template-columns: 1fr; }
}
</style>
