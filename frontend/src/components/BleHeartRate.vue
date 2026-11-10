<script setup lang="ts">
// Optional Bluetooth (BLE) heart-rate receiver (Modules page).
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { Bluetooth, Copy, Heart, Search } from 'lucide-vue-next'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { toast } from '@/dialog'

const { t } = useI18n()
interface Dev { address: string; name: string; rssi: number; heart_rate: boolean }
const st = ref<any>(null)
const enabled = ref(false)
const minBpm = ref(40)
const maxBpm = ref(200)
const sendVR = ref(true)
const busy = ref(false)
const showAll = ref(false)

const devices = computed<Dev[]>(() => (st.value?.devices || []).filter((d: Dev) => showAll.value || d.heart_rate))

async function load() {
  try {
    st.value = await api('/api/v1/ble_hr')
    enabled.value = st.value.enabled
    minBpm.value = st.value.min_bpm
    maxBpm.value = st.value.max_bpm
    sendVR.value = st.value.send_to_vrchat
  } catch {}
}
async function refresh() { try { st.value = await api('/api/v1/ble_hr') } catch {} }

async function save(extra: Record<string, unknown> = {}) {
  busy.value = true
  try {
    const r = await apiPost('/api/v1/ble_hr', {
      enabled: enabled.value, min_bpm: minBpm.value, max_bpm: maxBpm.value, send_to_vrchat: sendVR.value, ...extra,
    })
    if (r.detail) { toast(r.detail, 'error'); return }
    st.value = r
  } finally { busy.value = false }
}
async function scan() {
  const r = await apiPost('/api/v1/ble_hr/scan', {})
  if (r.detail) toast(r.detail, 'error')
  else st.value = r
}
function pick(d: Dev) { save({ device: d.address, device_name: d.name || d.address }) }
function auto() { save({ device: '', device_name: '' }) }
async function copy(s: string) { try { await navigator.clipboard.writeText(s); toast(t('dashboard.copied'), 'success') } catch {} }
const short = (p: string) => p.replace('/avatar/parameters/', '')
const statusText = computed(() => {
  const s = st.value
  if (!s) return ''
  if (s.status === 'connected') return t('ble.connected', { name: s.connected_to, bpm: s.bpm })
  if (s.status === 'error') return s.error
  if (s.status === 'connecting') return t('ble.connecting', { name: s.connected_to })
  if (s.status === 'scanning') return t('ble.searching') + (s.error ? ` (${s.error})` : '')
  return t('ble.off')
})

let timer = 0
const start = () => { clearInterval(timer); timer = window.setInterval(refresh, 1500) }
onMounted(() => { load(); start() })
onActivated(start)
onDeactivated(() => clearInterval(timer))
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="card">
    <h2 class="head"><Bluetooth :size="18" />{{ t('ble.title') }} <span class="opt">{{ t('pulsoid.optional') }}</span></h2>
    <div class="field">
      <label class="toggle-row">
        <input type="checkbox" class="switch" role="switch" v-model="enabled" :disabled="busy" @change="save()">
        <span>{{ t('ble.enable') }}</span>
      </label>
      <p class="hint">{{ t('ble.desc') }}</p>
    </div>
    <template v-if="enabled">
      <div class="status" :class="{ ok: st?.status === 'connected', err: st?.status === 'error' }">
        <Heart v-if="st?.status === 'connected'" :size="14" class="beat" />{{ statusText }}
      </div>

      <div class="field">
        <label>{{ t('ble.device') }}</label>
        <div class="row">
          <span class="current">{{ st?.device ? (st.device_name || st.device) : t('ble.autoDevice') }}</span>
          <button v-if="st?.device" class="btn btn-ghost btn-sm" :disabled="busy" @click="auto">{{ t('ble.useAuto') }}</button>
          <button class="btn btn-ghost btn-sm" :disabled="st?.scanning" @click="scan"><Search :size="13" />{{ st?.scanning ? t('ble.scanning') : t('ble.scan') }}</button>
        </div>
        <div v-if="(st?.devices || []).length" class="devices">
          <button v-for="d in devices" :key="d.address" class="dev" :class="{ sel: d.address === st?.device }" @click="pick(d)">
            <Heart v-if="d.heart_rate" :size="13" class="hr" />
            <span class="name">{{ d.name || t('ble.unnamed') }}</span>
            <span class="addr">{{ d.address }}</span>
            <span class="rssi">{{ d.rssi }} dBm</span>
          </button>
          <label class="chk"><input type="checkbox" v-model="showAll">{{ t('ble.showAll') }}</label>
        </div>
        <p class="hint">{{ t('ble.deviceHint') }}</p>
      </div>

      <div class="field">
        <label class="inline">{{ t('pulsoid.range') }}
          <input v-model.number="minBpm" type="number" min="0" max="300" class="num" @change="save()">
          –
          <input v-model.number="maxBpm" type="number" min="0" max="300" class="num" @change="save()"> BPM
        </label>
      </div>
      <div class="field">
        <label class="toggle-row">
          <input type="checkbox" class="switch" role="switch" v-model="sendVR" @change="save()">
          <span>{{ t('pulsoid.sendVR') }}</span>
        </label>
      </div>
      <p class="hint">{{ t('pulsoid.paramsHint') }}</p>
      <div class="params">
        <button v-for="p in st?.params || []" :key="p" class="chip" :title="t('monitor.copyAddress')" @click="copy(short(p))">
          {{ short(p) }} <Copy :size="11" />
        </button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.head { display: flex; align-items: center; gap: var(--sp-2); }
.opt { font-size: var(--text-2xs); font-weight: 500; color: var(--text-muted); border: 1px solid var(--border); border-radius: var(--radius-full); padding: 1px 8px; }
.field { margin-bottom: var(--sp-3); }
.field > label:not(.toggle-row):not(.inline) { display: block; font-size: var(--text-sm); color: var(--text-label); margin-bottom: var(--sp-1); font-weight: 500; }
.toggle-row { display: inline-flex; align-items: center; gap: var(--sp-2); font-size: var(--text-sm); cursor: pointer; }
.row { display: flex; align-items: center; gap: var(--sp-2); flex-wrap: wrap; }
.current { flex: 1; min-width: 0; font-size: var(--text-sm); color: var(--text); }
.inline { display: inline-flex; align-items: center; gap: var(--sp-2); font-size: var(--text-sm); color: var(--text-label); }
.num { width: 72px; text-align: center; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-1); }
.status { display: flex; align-items: center; gap: 6px; font-size: var(--text-sm); color: var(--text-muted); margin: 0 0 var(--sp-3); }
.status.ok { color: var(--success); }
.status.err { color: var(--danger); }
.beat { animation: beat 1s ease-in-out infinite; }
@keyframes beat { 50% { transform: scale(1.25); } }
.devices { display: flex; flex-direction: column; gap: 4px; margin-top: var(--sp-2); }
.dev { display: flex; align-items: center; gap: var(--sp-2); padding: 6px var(--sp-3); border: 1px solid var(--border); border-radius: var(--radius-sm); background: transparent; color: var(--text); cursor: pointer; text-align: left; font-size: var(--text-sm); }
.dev:hover { border-color: var(--border-hover); background: var(--bg-hover); }
.dev.sel { border-color: var(--accent); background: var(--bg-active); }
.dev .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dev .addr, .dev .rssi { font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--text-muted); }
.hr { color: var(--danger); flex-shrink: 0; }
.chk { display: inline-flex; align-items: center; gap: 6px; font-size: var(--text-xs); color: var(--text-muted); cursor: pointer; margin-top: 2px; }
.params { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.chip { display: inline-flex; align-items: center; gap: 4px; padding: 3px 10px; border: 1px solid var(--border); border-radius: var(--radius-full); background: var(--bg-tertiary); font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text); cursor: pointer; }
.chip:hover { border-color: var(--border-hover); }
</style>
