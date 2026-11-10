<script setup lang="ts">
// Parameter monitor: which PCS / OGB (or other configured) OSC parameters
// appeared, their value ranges and a timestamped change history.
import { computed, nextTick, onActivated, onDeactivated, onUnmounted, ref, watch } from 'vue'
import { Download, Trash2, Search, Copy, X, Plus } from 'lucide-vue-next'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { confirmDialog, toast } from '@/dialog'
import { themeColor } from '@/canvasFont'
import AppSelect from '@/components/AppSelect.vue'

const { t, locale } = useI18n()

interface MonParam {
  address: string; category: string; type: string
  first_seen: number; last_seen: number; count: number; changes: number
  last_value: unknown; min?: number; max?: number; history_len: number
  declared?: boolean; avatar?: string
}
interface Point { t: number; v: unknown }
interface Change { address: string; t: number; v: unknown }

const params = ref<MonParam[]>([])
const categories = ref<Record<string, number>>({})
const enabled = ref(true)
const prefixes = ref<string[]>([])
const newPrefix = ref('')
const serverNow = ref(Date.now() / 1000)
const q = ref('')
const cat = ref('')
const sortBy = ref<'last' | 'first' | 'name' | 'changes'>('last')
const selected = ref<string | null>(null)
const points = ref<Point[]>([])
const changes = ref<Change[]>([])
const live = ref(true)
const canvasRef = ref<HTMLCanvasElement | null>(null)

const PREFIX = '/avatar/parameters/'
const short = (a: string) => (a.startsWith(PREFIX) ? a.slice(PREFIX.length) : a)

const sorted = computed(() => {
  const list = params.value.filter((p) => (!cat.value || p.category === cat.value) &&
    (!q.value || p.address.toLowerCase().includes(q.value.toLowerCase())))
  const by: Record<string, (a: MonParam, b: MonParam) => number> = {
    last: (a, b) => b.last_seen - a.last_seen,
    first: (a, b) => a.first_seen - b.first_seen,
    name: (a, b) => a.address.localeCompare(b.address),
    changes: (a, b) => b.changes - a.changes,
  }
  return [...list].sort(by[sortBy.value])
})
const selectedParam = computed(() => params.value.find((p) => p.address === selected.value) || null)

async function load() {
  try {
    const d = await api('/api/v1/monitor')
    params.value = d.params || []
    categories.value = d.categories || {}
    enabled.value = d.enabled !== false
    prefixes.value = d.prefixes || []
    serverNow.value = d.now || Date.now() / 1000
  } catch {}
  try { changes.value = (await api('/api/v1/monitor/changes?limit=100')).changes || [] } catch {}
  if (selected.value) await loadHistory()
}

async function loadHistory() {
  if (!selected.value) return
  try {
    points.value = (await api('/api/v1/monitor/history?address=' + encodeURIComponent(selected.value))).points || []
  } catch { points.value = [] }
  await nextTick()
  draw()
}

function select(addr: string) {
  selected.value = selected.value === addr ? null : addr
  points.value = []
  if (selected.value) loadHistory()
}

async function saveSettings() {
  await apiPost('/api/v1/monitor/settings', { prefixes: prefixes.value })
  load()
}
function addPrefix() {
  const p = newPrefix.value.trim()
  if (!p) return
  const full = p.startsWith('/') ? p : PREFIX + p
  if (!prefixes.value.includes(full)) prefixes.value.push(full)
  newPrefix.value = ''
  saveSettings()
}
function removePrefix(i: number) { prefixes.value.splice(i, 1); saveSettings() }

async function clearAll() {
  if (!(await confirmDialog({ title: t('monitor.clearTitle'), message: t('monitor.clearBody'), confirmText: t('monitor.clear'), danger: true }))) return
  await apiPost('/api/v1/monitor/clear', {})
  selected.value = null
  load()
}
function exportJson() { window.open('/api/v1/monitor/export', '_blank') }

async function copy(text: string) {
  try { await navigator.clipboard.writeText(text); toast(t('dashboard.copied'), 'success') } catch {}
}

// ---- formatting
function fmtValue(v: unknown): string {
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : v.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
  if (v === null || v === undefined) return '—'
  return String(v)
}
function fmtClock(ts: number): string {
  const d = new Date(ts * 1000)
  const hms = d.toLocaleTimeString(locale.value === 'zh' ? 'zh-CN' : 'en-GB', { hour12: false })
  return `${hms}.${String(d.getMilliseconds()).padStart(3, '0')}`
}
function fmtDateTime(ts: number): string {
  return new Date(ts * 1000).toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-GB', { hour12: false })
}
function fmtAgo(ts: number): string {
  const s = Math.max(0, serverNow.value - ts)
  if (s < 2) return t('dashboard.justNow')
  if (s < 60) return `${Math.round(s)}s`
  if (s < 3600) return `${Math.round(s / 60)}min`
  if (s < 86400) return `${Math.round(s / 3600)}h`
  return `${Math.round(s / 86400)}d`
}

// ---- history chart (step chart: values hold until the next change)
function num(v: unknown): number | null {
  if (typeof v === 'boolean') return v ? 1 : 0
  if (typeof v === 'number' && Number.isFinite(v)) return v
  return null
}
function draw() {
  const c = canvasRef.value
  if (!c) return
  const dpr = window.devicePixelRatio || 1
  const W = c.clientWidth, H = c.clientHeight
  if (W < 1 || H < 1) return
  c.width = W * dpr; c.height = H * dpr
  const ctx = c.getContext('2d')!
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.fillStyle = themeColor('--canvas-bg'); ctx.fillRect(0, 0, W, H)
  const pts = points.value.map((p) => ({ t: p.t, v: num(p.v) })).filter((p) => p.v !== null) as { t: number; v: number }[]
  if (!pts.length) return
  let lo = Math.min(...pts.map((p) => p.v)), hi = Math.max(...pts.map((p) => p.v))
  if (lo >= 0 && hi <= 1) { lo = 0; hi = 1 }
  if (hi === lo) { hi = lo + 1 }
  const t0 = pts[0].t, t1 = Math.max(serverNow.value, pts[pts.length - 1].t)
  const span = Math.max(t1 - t0, 1)
  const pad = 6
  const X = (ts: number) => pad + (ts - t0) / span * (W - 2 * pad)
  const Y = (v: number) => H - pad - (v - lo) / (hi - lo) * (H - 2 * pad)
  ctx.strokeStyle = themeColor('--canvas-grid'); ctx.lineWidth = 1
  for (const f of [0.25, 0.5, 0.75]) { const y = pad + f * (H - 2 * pad); ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke() }
  ctx.strokeStyle = themeColor('--accent'); ctx.lineWidth = 1.5
  ctx.beginPath()
  pts.forEach((p, i) => {
    if (i === 0) ctx.moveTo(X(p.t), Y(p.v))
    else { ctx.lineTo(X(p.t), Y(pts[i - 1].v)); ctx.lineTo(X(p.t), Y(p.v)) }
  })
  ctx.lineTo(X(t1), Y(pts[pts.length - 1].v))
  ctx.stroke()
  ctx.fillStyle = themeColor('--text-muted'); ctx.font = '10px sans-serif'
  ctx.fillText(fmtValue(hi), 4, 12); ctx.fillText(fmtValue(lo), 4, H - 4)
}
watch(points, () => nextTick(draw))
window.addEventListener('resize', draw)
window.addEventListener('themechange', draw)

// ---- live refresh while the page is shown
let timer = 0
function start() { load(); clearInterval(timer); timer = window.setInterval(() => { if (live.value) load() }, 1000) }
function stop() { clearInterval(timer) }
onActivated(start)
onDeactivated(stop)
onUnmounted(() => {
  stop()
  window.removeEventListener('resize', draw)
  window.removeEventListener('themechange', draw)
})
</script>

<template>
  <div class="monitor">
    <h1 class="gradient-text page-title">{{ t('monitor.title') }}</h1>
    <p class="page-desc">{{ t('monitor.desc') }}</p>

    <section class="card settings">
      <span class="state" :class="{ on: enabled }">
        {{ enabled ? t('monitor.stateOn') : t('monitor.stateOff') }}
        <router-link v-if="!enabled" to="/settings">{{ t('monitor.enableInSettings') }} →</router-link>
      </span>
      <div class="prefixes">
        <span class="lbl">{{ t('monitor.prefixes') }}</span>
        <span v-for="(p, i) in prefixes" :key="p" class="chip" :title="p">
          {{ short(p) || p }}
          <button class="chip-x" :aria-label="t('common.delete')" :title="t('common.delete')" @click="removePrefix(i)"><X :size="12" /></button>
        </span>
        <div class="add-form">
          <div class="add-path">
            <span class="prefix-label">/avatar/parameters/</span>
            <input v-model="newPrefix" type="text" class="add-input" placeholder="pcs/" :aria-label="t('monitor.prefixes')" @keyup.enter="addPrefix">
          </div>
          <button class="btn btn-ghost" :disabled="!newPrefix.trim()" @click="addPrefix"><Plus :size="15" />{{ t('common.add') }}</button>
        </div>
      </div>
      <div class="actions">
        <label class="toggle-row"><input type="checkbox" v-model="live">{{ t('monitor.live') }}</label>
        <button class="btn btn-ghost btn-sm" @click="exportJson"><Download :size="14" />{{ t('monitor.export') }}</button>
        <button class="btn btn-ghost btn-sm btn-ghost-danger" @click="clearAll"><Trash2 :size="14" />{{ t('monitor.clear') }}</button>
      </div>
    </section>

    <div class="toolbar">
      <div class="search"><Search :size="14" class="search-icon" /><input v-model="q" type="search" :placeholder="t('monitor.search')" :aria-label="t('monitor.search')"></div>
      <div class="cats">
        <button :class="{ active: !cat }" @click="cat = ''">{{ t('monitor.all') }} ({{ params.length }})</button>
        <button v-for="(n, c) in categories" :key="c" :class="{ active: cat === c }" @click="cat = String(c)">{{ String(c).toUpperCase() }} ({{ n }})</button>
      </div>
      <AppSelect v-model="sortBy" small class="sort" :aria-label="t('monitor.sort')" :options="[
        { value: 'last', label: t('monitor.sortLast') }, { value: 'first', label: t('monitor.sortFirst') },
        { value: 'name', label: t('monitor.sortName') }, { value: 'changes', label: t('monitor.sortChanges') }]" />
    </div>

    <div class="layout">
      <section class="card table-card">
        <table>
          <thead>
            <tr>
              <th>{{ t('monitor.colParam') }}</th>
              <th>{{ t('monitor.colValue') }}</th>
              <th>{{ t('monitor.colRange') }}</th>
              <th class="num">{{ t('monitor.colChanges') }}</th>
              <th>{{ t('monitor.colFirst') }}</th>
              <th>{{ t('monitor.colLast') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in sorted" :key="p.address" :class="{ sel: p.address === selected, fresh: serverNow - p.last_seen < 2 }"
              tabindex="0" @click="select(p.address)" @keyup.enter="select(p.address)">
              <td class="addr" :title="p.address + (p.avatar ? ' · ' + p.avatar : '')"><span class="cat-tag" :class="'cat-' + p.category">{{ p.category }}</span>{{ short(p.address) }}</td>
              <td class="mono">{{ p.count ? fmtValue(p.last_value) : '—' }} <span class="type">{{ p.type }}</span></td>
              <td class="mono muted">{{ p.min !== undefined ? `${fmtValue(p.min)} ~ ${fmtValue(p.max)}` : '—' }}</td>
              <td class="num mono" :title="t('monitor.countHint', { n: p.count })">{{ p.changes }}</td>
              <td class="muted" :title="fmtDateTime(p.first_seen)">{{ p.count ? fmtDateTime(p.first_seen) : '—' }}</td>
              <td :title="p.count ? fmtDateTime(p.last_seen) : t('monitor.declaredHint', { avatar: p.avatar || '' })">
                <span class="live-dot" />{{ p.count ? fmtAgo(p.last_seen) : t('monitor.notReceived') }}</td>
            </tr>
            <tr v-if="!sorted.length"><td colspan="6" class="empty">{{ t('monitor.empty') }}</td></tr>
          </tbody>
        </table>
      </section>

      <aside class="side">
        <section v-if="selectedParam" class="card">
          <div class="detail-head">
            <h2 :title="selectedParam.address">{{ short(selectedParam.address) }}</h2>
            <button class="icon-action" :title="t('monitor.copyAddress')" :aria-label="t('monitor.copyAddress')" @click="copy(selectedParam.address)"><Copy :size="14" /></button>
            <button class="icon-action" :aria-label="t('common.cancel')" @click="select(selectedParam.address)"><X :size="14" /></button>
          </div>
          <div class="meta">
            <span>{{ t('monitor.metaCount', { n: selectedParam.count }) }}</span>
            <span>{{ t('monitor.metaChanges', { n: selectedParam.changes, kept: points.length }) }}</span>
          </div>
          <canvas ref="canvasRef" class="chart"></canvas>
          <div class="points">
            <div v-for="(pt, i) in [...points].reverse()" :key="i" class="pt-row">
              <span class="mono muted">{{ fmtClock(pt.t) }}</span>
              <span class="mono">{{ fmtValue(pt.v) }}</span>
            </div>
          </div>
        </section>
        <section class="card">
          <h2>{{ t('monitor.recent') }}</h2>
          <div class="points">
            <div v-for="(c, i) in changes" :key="i" class="pt-row clickable" @click="select(c.address)">
              <span class="mono muted">{{ fmtClock(c.t) }}</span>
              <span class="addr-sm" :title="c.address">{{ short(c.address) }}</span>
              <span class="mono">{{ fmtValue(c.v) }}</span>
            </div>
            <div v-if="!changes.length" class="empty">{{ t('monitor.empty') }}</div>
          </div>
        </section>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.state { font-size: var(--text-sm); color: var(--text-muted); }
.state.on { color: var(--success); }
.settings { display: flex; flex-wrap: wrap; align-items: center; gap: var(--sp-4); margin-bottom: var(--sp-4); }
.toggle-row { display: inline-flex; align-items: center; gap: var(--sp-2); font-size: var(--text-sm); color: var(--text-label); cursor: pointer; }
.prefixes { display: flex; flex-wrap: wrap; align-items: center; gap: var(--sp-2); flex: 1; min-width: 300px; }
.lbl { font-size: var(--text-sm); color: var(--text-muted); }
.chip { display: inline-flex; align-items: center; gap: 2px; padding: 4px 4px 4px 10px; border: 1px solid var(--border); border-radius: var(--radius-full); background: var(--bg-tertiary); font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text); }
.chip-x { display: inline-flex; background: none; border: none; color: var(--text-muted); cursor: pointer; padding: 2px; }
.chip-x:hover { color: var(--danger); }
/* same input group as "add parameter" on the parameter page */
.add-form { display: flex; gap: var(--sp-2); align-items: center; }
.add-path { display: flex; min-width: 0; }
.prefix-label { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); white-space: nowrap; background: var(--bg-tertiary); padding: var(--sp-2); border-radius: var(--radius-sm) 0 0 var(--radius-sm); border: 1px solid var(--border); border-right: none; display: flex; align-items: center; }
.add-input { width: 160px; padding: var(--sp-2) var(--sp-3); border-radius: 0 var(--radius-md) var(--radius-md) 0; font-family: var(--font-mono); font-size: var(--text-xs); }
.actions { display: flex; align-items: center; gap: var(--sp-2); margin-left: auto; }
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-3); }
.search { position: relative; display: flex; align-items: center; }
.search-icon { position: absolute; left: 10px; color: var(--text-muted); pointer-events: none; }
.search input { width: 260px; padding: var(--sp-2) var(--sp-3) var(--sp-2) 30px; font-size: var(--text-sm); }
.cats { display: flex; flex-wrap: wrap; gap: var(--sp-1); }
.cats button { padding: 2px 10px; border: 1px solid var(--border); border-radius: var(--radius-full); background: transparent; color: var(--text-muted); cursor: pointer; font-size: var(--text-xs); }
.cats button.active { border-color: var(--accent); color: var(--text); background: var(--bg-active); }
.sort { margin-left: auto; width: 150px; min-width: 0; }
.layout { display: grid; grid-template-columns: minmax(0, 1fr) 340px; gap: var(--sp-4); align-items: start; }
.table-card { padding: 0; overflow: auto; max-height: calc(100vh - 320px); }
table { width: 100%; border-collapse: collapse; font-size: var(--text-sm); }
th { position: sticky; top: 0; background: var(--bg-card); text-align: left; font-weight: 500; color: var(--text-muted); font-size: var(--text-xs); padding: var(--sp-2) var(--sp-3); border-bottom: 1px solid var(--border); }
td { padding: 6px var(--sp-3); border-bottom: 1px solid var(--border-subtle, var(--border)); white-space: nowrap; }
tbody tr { cursor: pointer; }
tbody tr:hover { background: var(--bg-hover); }
tbody tr.sel { background: var(--bg-active); }
.num { text-align: right; }
.mono { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.muted { color: var(--text-muted); }
.type { font-size: var(--text-2xs); color: var(--text-muted); margin-left: 4px; }
.addr { max-width: 360px; overflow: hidden; text-overflow: ellipsis; font-family: var(--font-mono); font-size: var(--text-xs); }
.cat-tag { display: inline-block; min-width: 34px; margin-right: 6px; padding: 0 5px; border-radius: var(--radius-sm); font-size: var(--text-2xs); text-transform: uppercase; text-align: center; background: var(--bg-tertiary); color: var(--text-secondary); }
.cat-tag.cat-ogb { background: rgba(var(--yellow-rgb), 0.18); color: var(--warning); }
.cat-tag.cat-pcs { background: rgba(var(--accent-rgb), 0.15); color: var(--accent); }
.live-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--text-muted); opacity: 0.35; margin-right: 6px; vertical-align: middle; }
tr.fresh .live-dot { background: var(--success); opacity: 1; }
.empty { text-align: center; color: var(--text-muted); padding: var(--sp-5); }
.side { display: flex; flex-direction: column; gap: var(--sp-4); }
.detail-head { display: flex; align-items: center; gap: var(--sp-2); }
.detail-head h2 { flex: 1; min-width: 0; margin: 0; font-size: var(--text-sm); font-family: var(--font-mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meta { display: flex; gap: var(--sp-3); font-size: var(--text-xs); color: var(--text-muted); margin: var(--sp-2) 0; }
.chart { width: 100%; height: 120px; border-radius: var(--radius-md); display: block; margin-bottom: var(--sp-2); }
.points { max-height: 280px; overflow-y: auto; display: flex; flex-direction: column; }
.pt-row { display: flex; gap: var(--sp-3); padding: 3px var(--sp-1); font-size: var(--text-xs); }
.pt-row.clickable { cursor: pointer; border-radius: var(--radius-sm); }
.pt-row.clickable:hover { background: var(--bg-hover); }
.pt-row .mono:last-child { margin-left: auto; }
.addr-sm { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-mono); }
@media (max-width: 1100px) { .layout { grid-template-columns: 1fr; } }
</style>
