<script setup lang="ts">
// Optional Pulsoid heart rate (Settings). The token is write-only: the
// server only ever returns whether one is stored and its last 4 chars.
import { onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { HeartPulse, Copy } from 'lucide-vue-next'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { toast } from '@/dialog'

const { t } = useI18n()
const st = ref<any>(null)
const enabled = ref(false)
const token = ref('')
const minBpm = ref(40)
const maxBpm = ref(200)
const sendVR = ref(true)
const busy = ref(false)

async function load() {
  try {
    st.value = await api('/api/v1/pulsoid')
    enabled.value = st.value.enabled
    minBpm.value = st.value.min_bpm
    maxBpm.value = st.value.max_bpm
    sendVR.value = st.value.send_to_vrchat
  } catch {}
}
async function refreshStatus() { try { st.value = { ...st.value, ...(await api('/api/v1/pulsoid')) } } catch {} }

async function save(extra: Record<string, unknown> = {}) {
  busy.value = true
  try {
    const r = await apiPost('/api/v1/pulsoid', {
      enabled: enabled.value, min_bpm: minBpm.value, max_bpm: maxBpm.value, send_to_vrchat: sendVR.value,
      ...(token.value.trim() ? { token: token.value.trim() } : {}), ...extra,
    })
    if (r.detail) { toast(r.detail, 'error'); return }
    st.value = r
    token.value = ''
  } finally { busy.value = false }
}
async function copy(s: string) { try { await navigator.clipboard.writeText(s); toast(t('dashboard.copied'), 'success') } catch {} }
const short = (p: string) => p.replace('/avatar/parameters/', '')

let timer = 0
const start = () => { clearInterval(timer); timer = window.setInterval(refreshStatus, 2000) }
onMounted(() => { load(); start() })
onActivated(start)
onDeactivated(() => clearInterval(timer))
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="card">
    <h2 class="head"><HeartPulse :size="18" />{{ t('pulsoid.title') }} <span class="opt">{{ t('pulsoid.optional') }}</span></h2>
    <div class="field">
      <label class="toggle-row">
        <input type="checkbox" class="switch" role="switch" v-model="enabled" :disabled="busy" @change="save()">
        <span>{{ t('pulsoid.enable') }}</span>
      </label>
      <p class="hint">{{ t('pulsoid.desc') }}</p>
    </div>
    <template v-if="enabled">
      <div class="field">
        <label>{{ t('pulsoid.token') }}</label>
        <div class="row">
          <input v-model="token" type="password" autocomplete="off" class="grow"
            :placeholder="st?.has_token ? t('pulsoid.tokenSaved', { hint: st.token_hint }) : t('pulsoid.tokenPlaceholder')">
          <button class="btn btn-ghost" :disabled="busy || !token.trim()" @click="save()">{{ t('common.save') }}</button>
        </div>
        <p class="hint">
          {{ t('pulsoid.tokenHint') }}
          <a href="https://pulsoid.net/ui/keys" target="_blank" rel="noopener">pulsoid.net/ui/keys</a>
        </p>
      </div>
      <div class="field row">
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
        <p class="hint">{{ t('pulsoid.sendVRHint') }}</p>
      </div>
      <div class="status" :class="{ ok: st?.connected, err: st?.error }">
        <template v-if="st?.connected">● {{ t('pulsoid.connected', { bpm: st.bpm }) }}</template>
        <template v-else-if="st?.error">{{ st.error }}</template>
        <template v-else-if="!st?.has_token">{{ t('pulsoid.needToken') }}</template>
        <template v-else>{{ t('pulsoid.connecting') }}</template>
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
.row { display: flex; align-items: center; gap: var(--sp-2); }
.grow { flex: 1; min-width: 0; font-family: var(--font-mono); }
.inline { display: inline-flex; align-items: center; gap: var(--sp-2); font-size: var(--text-sm); color: var(--text-label); }
.num { width: 72px; text-align: center; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-1); }
.status { font-size: var(--text-sm); color: var(--text-muted); margin: var(--sp-2) 0; }
.status.ok { color: var(--success); }
.status.err { color: var(--danger); }
.params { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.chip { display: inline-flex; align-items: center; gap: 4px; padding: 3px 10px; border: 1px solid var(--border); border-radius: var(--radius-full); background: var(--bg-tertiary); font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text); cursor: pointer; }
.chip:hover { border-color: var(--border-hover); }
</style>
