<script setup lang="ts">
// Optional HypeRate heart rate (Modules page). Only the session ID is
// needed day to day; the endpoint and channel are filled in. The API key
// is write-only (the server returns whether one is stored + last 4 chars).
import { onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { Activity, Copy } from 'lucide-vue-next'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { toast } from '@/dialog'

const { t } = useI18n()
const st = ref<any>(null)
const enabled = ref(false)
const sessionId = ref('')
const apiKey = ref('')
const editKey = ref(false)
const minBpm = ref(40)
const maxBpm = ref(200)
const sendVR = ref(true)
const busy = ref(false)

async function load() {
  try {
    st.value = await api('/api/v1/hyperate')
    enabled.value = st.value.enabled
    sessionId.value = st.value.session_id
    minBpm.value = st.value.min_bpm
    maxBpm.value = st.value.max_bpm
    sendVR.value = st.value.send_to_vrchat
  } catch {}
}
async function refresh() { try { st.value = await api('/api/v1/hyperate') } catch {} }

/** Accept a pasted share link: keep only the ID part. */
function onIdInput() {
  const v = sessionId.value.trim()
  if (v.includes('/')) sessionId.value = v.replace(/[?#].*$/, '').replace(/\/+$/, '').split('/').pop() || ''
}

async function save(extra: Record<string, unknown> = {}) {
  busy.value = true
  try {
    const r = await apiPost('/api/v1/hyperate', {
      enabled: enabled.value, session_id: sessionId.value, min_bpm: minBpm.value, max_bpm: maxBpm.value,
      send_to_vrchat: sendVR.value, ...(apiKey.value.trim() ? { api_key: apiKey.value.trim() } : {}), ...extra,
    })
    if (r.detail) { toast(r.detail, 'error'); return }
    st.value = r
    sessionId.value = r.session_id
    apiKey.value = ''
    editKey.value = false
  } finally { busy.value = false }
}
async function copy(s: string) { try { await navigator.clipboard.writeText(s); toast(t('dashboard.copied'), 'success') } catch {} }
const short = (p: string) => p.replace('/avatar/parameters/', '')

let timer = 0
const start = () => { clearInterval(timer); timer = window.setInterval(refresh, 2000) }
onMounted(() => { load(); start() })
onActivated(start)
onDeactivated(() => clearInterval(timer))
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="card">
    <h2 class="head"><Activity :size="18" />{{ t('hyperate.title') }} <span class="opt">{{ t('pulsoid.optional') }}</span></h2>
    <div class="field">
      <label class="toggle-row">
        <input type="checkbox" class="switch" role="switch" v-model="enabled" :disabled="busy" @change="save()">
        <span>{{ t('hyperate.enable') }}</span>
      </label>
      <p class="hint">{{ t('hyperate.desc') }}</p>
    </div>
    <template v-if="enabled">
      <div class="field">
        <label>{{ t('hyperate.session') }}</label>
        <div class="row">
          <div class="group">
            <span class="prefix">app.hyperate.io/</span>
            <input v-model="sessionId" type="text" class="id" placeholder="ABCD" spellcheck="false"
              :aria-label="t('hyperate.session')" @input="onIdInput" @keyup.enter="save()" @change="save()">
          </div>
        </div>
        <p class="hint">{{ t('hyperate.sessionHint') }}</p>
      </div>

      <div class="field">
        <label>{{ t('hyperate.apiKey') }}</label>
        <div v-if="st?.has_key && !editKey" class="row">
          <span class="saved">{{ t('hyperate.keySaved', { hint: st.key_hint }) }}</span>
          <button class="btn btn-ghost btn-sm" @click="editKey = true">{{ t('hyperate.changeKey') }}</button>
        </div>
        <div v-else class="row">
          <input v-model="apiKey" type="password" autocomplete="off" spellcheck="false" class="grow key"
            :aria-label="t('hyperate.apiKey')" :placeholder="t('hyperate.keyPlaceholder')" @keyup.enter="apiKey.trim() && save()">
          <button class="btn btn-primary" :disabled="busy || !apiKey.trim()" @click="save()">{{ t('common.save') }}</button>
          <button v-if="st?.has_key" class="btn btn-ghost" @click="editKey = false; apiKey = ''">{{ t('common.cancel') }}</button>
        </div>
        <p class="hint">{{ t('hyperate.keyHint') }}</p>
      </div>

      <div class="status" :class="{ ok: st?.connected, err: st?.error }">
        <template v-if="st?.connected && st?.bpm">● {{ t('pulsoid.connected', { bpm: st.bpm }) }}</template>
        <template v-else-if="st?.connected">● {{ t('hyperate.waiting') }}</template>
        <template v-else-if="st?.error">{{ st.error }}</template>
        <template v-else-if="!st?.session_id">{{ t('hyperate.needSession') }}</template>
        <template v-else-if="!st?.has_key">{{ t('hyperate.needKey') }}</template>
        <template v-else>{{ t('pulsoid.connecting') }}</template>
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
/* same prefixed input group as the parameter page */
.group { display: flex; flex: 1; min-width: 0; }
.prefix { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); white-space: nowrap; background: var(--bg-tertiary); padding: var(--sp-2); border: 1px solid var(--border); border-right: none; border-radius: var(--radius-sm) 0 0 var(--radius-sm); display: flex; align-items: center; }
.id { flex: 1; min-width: 0; border-radius: 0 var(--radius-md) var(--radius-md) 0; font-family: var(--font-mono); text-transform: none; }
.grow { flex: 1; min-width: 0; }
.key { font-family: var(--font-mono); }
.key::placeholder { font-family: var(--font); }
.saved { flex: 1; font-size: var(--text-sm); color: var(--text-secondary); }
.inline { display: inline-flex; align-items: center; gap: var(--sp-2); font-size: var(--text-sm); color: var(--text-label); }
.num { width: 72px; text-align: center; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-1); }
.status { font-size: var(--text-sm); color: var(--text-muted); margin: var(--sp-2) 0 var(--sp-3); }
.status.ok { color: var(--success); }
.status.err { color: var(--danger); }
.params { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.chip { display: inline-flex; align-items: center; gap: 4px; padding: 3px 10px; border: 1px solid var(--border); border-radius: var(--radius-full); background: var(--bg-tertiary); font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text); cursor: pointer; }
.chip:hover { border-color: var(--border-hover); }
</style>
