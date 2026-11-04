<script setup lang="ts">
import AppSelect from '@/components/AppSelect.vue'
import { Check, Pencil, Plus, RotateCcw, Trash2, X } from 'lucide-vue-next'
import { ref, onMounted, onActivated } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { onBeforeRouteLeave } from 'vue-router'
import { confirmDialog } from '@/dialog'

const { t } = useI18n()

interface Param {
  path: string
  mode: string
  enabled: boolean
}

const MODES = [
  { value: 'distance' },
  { value: 'shock' },
  { value: 'touch' },
  { value: 'combo' },
  { value: 'boost' },
]

const PARAM_PREFIX = '/avatar/parameters/'

// Per-channel state
const paramsA = ref<Param[]>([])
const paramsB = ref<Param[]>([])
const defaultModeA = ref('distance')
const defaultModeB = ref('distance')
const strengthLimitA = ref(100)
const strengthLimitB = ref(100)
const dirtyA = ref(false)
const dirtyB = ref(false)

// Add form state (shared)
const newPathA = ref('')
const newModeA = ref('distance')
const newPathB = ref('')
const newModeB = ref('distance')
const editIndexA = ref<number | null>(null)
const editIndexB = ref<number | null>(null)
const editPath = ref('')
const editMode = ref('')
const msg = ref('')
const msgErr = ref(false)

async function loadChannel(ch: 'a' | 'b') {
  const data = await api(`/api/v1/params/${ch}`)
  const params = (data.params || []).map((p: any) => ({
    path: typeof p === 'string' ? p : p.path,
    mode: typeof p === 'string' ? data.default_mode : (p.mode || data.default_mode),
    enabled: typeof p === 'string' ? true : (p.enabled !== false),
  }))
  if (ch === 'a') {
    paramsA.value = params
    defaultModeA.value = data.default_mode || 'distance'
    strengthLimitA.value = data.strength_limit ?? 100
    dirtyA.value = false
    editIndexA.value = null
  } else {
    paramsB.value = params
    defaultModeB.value = data.default_mode || 'distance'
    strengthLimitB.value = data.strength_limit ?? 100
    dirtyB.value = false
    editIndexB.value = null
  }
}

function addParam(ch: 'a' | 'b') {
  const rawPath = ch === 'a' ? newPathA.value.trim() : newPathB.value.trim()
  const mode = ch === 'a' ? newModeA.value : newModeB.value
  if (!rawPath) return
  let path = rawPath
  if (!path.startsWith('/')) path = PARAM_PREFIX + path
  const params = ch === 'a' ? paramsA : paramsB
  if (params.value.some(p => p.path === path)) { showMsg(t('params.paramExists'), true); return }
  params.value.push({ path, mode, enabled: true })
  if (ch === 'a') { newPathA.value = ''; dirtyA.value = true }
  else { newPathB.value = ''; dirtyB.value = true }
}

function removeParam(ch: 'a' | 'b', index: number) {
  const params = ch === 'a' ? paramsA : paramsB
  const editIdx = ch === 'a' ? editIndexA : editIndexB
  params.value.splice(index, 1)
  if (ch === 'a') dirtyA.value = true; else dirtyB.value = true
  if (editIdx.value === index) editIdx.value = null
}

function startEdit(ch: 'a' | 'b', index: number) {
  const params = ch === 'a' ? paramsA : paramsB
  if (ch === 'a') editIndexA.value = index; else editIndexB.value = index
  editPath.value = params.value[index].path
  editMode.value = params.value[index].mode
}

function confirmEdit(ch: 'a' | 'b') {
  const editIdx = ch === 'a' ? editIndexA : editIndexB
  const params = ch === 'a' ? paramsA : paramsB
  if (editIdx.value === null) return
  const path = editPath.value.trim()
  if (!path || !path.startsWith('/')) { showMsg(t('params.paramExists'), true); return }
  params.value[editIdx.value] = { path, mode: editMode.value, enabled: params.value[editIdx.value].enabled }
  editIdx.value = null
  if (ch === 'a') dirtyA.value = true; else dirtyB.value = true
}

function cancelEdit(ch: 'a' | 'b') {
  if (ch === 'a') editIndexA.value = null; else editIndexB.value = null
}

async function waitForRestart() {
  for (let i = 0; i < 20; i++) {
    await new Promise(r => setTimeout(r, 300))
    try { await fetch('/api/v1/status') } catch { break }
  }
  for (let i = 0; i < 30; i++) {
    await new Promise(r => setTimeout(r, 500))
    try { const resp = await fetch('/api/v1/status'); if (resp.ok) return } catch {}
  }
}

async function save(ch: 'a' | 'b') {
  const params = ch === 'a' ? paramsA.value : paramsB.value
  const defaultMode = ch === 'a' ? defaultModeA.value : defaultModeB.value
  // strength_limit is owned by the Strength page; never send a possibly stale copy.
  const data = await apiPost(`/api/v1/params/${ch}`, { params, default_mode: defaultMode })
  if (data.success) {
    showMsg(t('params.savedRestarting'), false)
    if (ch === 'a') dirtyA.value = false; else dirtyB.value = false
    await waitForRestart()
    loadChannel(ch)
    showMsg(t('params.applied'), false)
  } else {
    showMsg(data.message || t('common.saveFailed'), true)
  }
}

function showMsg(text: string, err: boolean) {
  msg.value = text; msgErr.value = err
  setTimeout(() => msg.value = '', 5000)
}

function modeLabel(mode: string) {
  const key = 'params.mode' + mode.charAt(0).toUpperCase() + mode.slice(1)
  const translated = t(key)
  return translated !== key ? translated : mode
}

function shortPath(path: string) {
  return path.startsWith(PARAM_PREFIX) ? path.slice(PARAM_PREFIX.length) : path
}

onMounted(() => { loadChannel('a'); loadChannel('b') })

// Pages are kept alive: refresh the read-only limits when coming back from
// the Strength page without discarding unsaved parameter edits.
onActivated(async () => {
  try {
    const d = await api('/api/v1/strength_limit')
    strengthLimitA.value = d.channel_a ?? strengthLimitA.value
    strengthLimitB.value = d.channel_b ?? strengthLimitB.value
  } catch {}
})

onBeforeRouteLeave(async () => {
  if (!dirtyA.value && !dirtyB.value) return true
  return confirmDialog({ title: t('common.unsavedTitle'), message: t('common.unsavedBody'), confirmText: t('common.discard'), danger: true })
})
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('params.title') }}</h1>
    <p class="subtitle">{{ t('params.desc') }}</p>

    <!-- Channel A -->
    <section class="channel-section">
      <div class="channel-header">
        <h2 class="ch-title ch-a">{{ t('common.channelA') }}</h2>
        <span v-if="dirtyA" class="dirty-badge">● {{ t('common.unsaved') }}</span>
      </div>

      <div class="settings-row">
        <div class="setting">
          <label>{{ t('params.defaultMode') }}</label>
          <AppSelect v-model="defaultModeA" @change="dirtyA = true" :options="[...(MODES).map((m) => ({ value: m.value, label: modeLabel(m.value) }))]" />
        </div>
        <div class="setting">
          <label>{{ t('params.strengthLimit') }}</label>
          <router-link to="/strength" class="readonly-value" :title="t('params.strengthLimitHint')">
            <span>{{ strengthLimitA }}</span><span class="readonly-link">{{ t('params.strengthLimitEdit') }} →</span>
          </router-link>
        </div>
      </div>

      <table>
        <thead>
          <tr><th style="width:36px">{{ t('params.colEnabled') }}</th><th>{{ t('params.colPath') }} <span class="th-prefix">/avatar/parameters/…</span></th><th>{{ t('params.colMode') }}</th><th style="width:80px">{{ t('params.colActions') }}</th></tr>
        </thead>
        <tbody>
          <tr v-for="(p, i) in paramsA" :key="i" :class="{'editing': editIndexA === i, 'disabled-row': !p.enabled}">
            <template v-if="editIndexA === i">
              <td><input type="checkbox" v-model="p.enabled" @change="dirtyA = true"></td>
              <td><input v-model="editPath" class="edit-input"></td>
              <td><AppSelect v-model="editMode" class="edit-select" :options="[...(MODES).map((m) => ({ value: m.value, label: modeLabel(m.value) }))]" /></td>
              <td class="actions"><button class="icon-action success" :title="t('common.save')" :aria-label="t('common.save')" @click="confirmEdit('a')"><Check :size="15" /></button><button class="icon-action" :title="t('common.cancel')" :aria-label="t('common.cancel')" @click="cancelEdit('a')"><X :size="15" /></button></td>
            </template>
            <template v-else>
              <td><input type="checkbox" v-model="p.enabled" @change="dirtyA = true"></td>
              <td class="path" :class="{'path-disabled': !p.enabled}" :title="p.path">{{ shortPath(p.path) }}</td>
              <td><span class="mode-badge" :class="'mode-' + p.mode">{{ modeLabel(p.mode) }}</span></td>
              <td class="actions"><button class="icon-action" :title="t('common.edit')" :aria-label="t('common.edit')" @click="startEdit('a', i)"><Pencil :size="15" /></button><button class="icon-action danger" :title="t('common.delete')" :aria-label="t('common.delete')" @click="removeParam('a', i)"><Trash2 :size="15" /></button></td>
            </template>
          </tr>
          <tr v-if="!paramsA.length"><td colspan="4" class="empty">{{ t('params.noParams') }}</td></tr>
        </tbody>
      </table>

      <div class="add-form" data-tour="param-add">
        <span class="prefix-label">/avatar/parameters/</span>
        <input v-model="newPathA" placeholder="pcs/contact/enterPass" class="add-input" @keyup.enter="addParam('a')">
        <AppSelect v-model="newModeA" class="add-select" data-tour="param-mode-select" :options="[...(MODES).map((m) => ({ value: m.value, label: modeLabel(m.value) }))]" />
        <button class="btn btn-success" @click="addParam('a')"><Plus :size="15" />{{ t('common.add') }}</button>
      </div>

      <div class="save-row">
        <button class="btn btn-primary" data-save data-tour="param-save" :disabled="!dirtyA" @click="save('a')">{{ t('common.save') }}</button>
        <span v-if="dirtyA" class="unsaved-badge">{{ t('common.unsavedBadge') }}</span>
        <button class="btn btn-ghost" @click="loadChannel('a')"><RotateCcw :size="14" />{{ t('params.undoChanges') }}</button>
      </div>
    </section>

    <!-- Channel B -->
    <section class="channel-section">
      <div class="channel-header">
        <h2 class="ch-title ch-b">{{ t('common.channelB') }}</h2>
        <span v-if="dirtyB" class="dirty-badge">● {{ t('common.unsaved') }}</span>
      </div>

      <div class="settings-row">
        <div class="setting">
          <label>{{ t('params.defaultMode') }}</label>
          <AppSelect v-model="defaultModeB" @change="dirtyB = true" :options="[...(MODES).map((m) => ({ value: m.value, label: modeLabel(m.value) }))]" />
        </div>
        <div class="setting">
          <label>{{ t('params.strengthLimit') }}</label>
          <router-link to="/strength" class="readonly-value" :title="t('params.strengthLimitHint')">
            <span>{{ strengthLimitB }}</span><span class="readonly-link">{{ t('params.strengthLimitEdit') }} →</span>
          </router-link>
        </div>
      </div>

      <table>
        <thead>
          <tr><th style="width:36px">{{ t('params.colEnabled') }}</th><th>{{ t('params.colPath') }} <span class="th-prefix">/avatar/parameters/…</span></th><th>{{ t('params.colMode') }}</th><th style="width:80px">{{ t('params.colActions') }}</th></tr>
        </thead>
        <tbody>
          <tr v-for="(p, i) in paramsB" :key="i" :class="{'editing': editIndexB === i, 'disabled-row': !p.enabled}">
            <template v-if="editIndexB === i">
              <td><input type="checkbox" v-model="p.enabled" @change="dirtyB = true"></td>
              <td><input v-model="editPath" class="edit-input"></td>
              <td><AppSelect v-model="editMode" class="edit-select" :options="[...(MODES).map((m) => ({ value: m.value, label: modeLabel(m.value) }))]" /></td>
              <td class="actions"><button class="icon-action success" :title="t('common.save')" :aria-label="t('common.save')" @click="confirmEdit('b')"><Check :size="15" /></button><button class="icon-action" :title="t('common.cancel')" :aria-label="t('common.cancel')" @click="cancelEdit('b')"><X :size="15" /></button></td>
            </template>
            <template v-else>
              <td><input type="checkbox" v-model="p.enabled" @change="dirtyB = true"></td>
              <td class="path" :class="{'path-disabled': !p.enabled}" :title="p.path">{{ shortPath(p.path) }}</td>
              <td><span class="mode-badge" :class="'mode-' + p.mode">{{ modeLabel(p.mode) }}</span></td>
              <td class="actions"><button class="icon-action" :title="t('common.edit')" :aria-label="t('common.edit')" @click="startEdit('b', i)"><Pencil :size="15" /></button><button class="icon-action danger" :title="t('common.delete')" :aria-label="t('common.delete')" @click="removeParam('b', i)"><Trash2 :size="15" /></button></td>
            </template>
          </tr>
          <tr v-if="!paramsB.length"><td colspan="4" class="empty">{{ t('params.noParams') }}</td></tr>
        </tbody>
      </table>

      <div class="add-form">
        <span class="prefix-label">/avatar/parameters/</span>
        <input v-model="newPathB" placeholder="pcs/contact/enterPass" class="add-input" @keyup.enter="addParam('b')">
        <AppSelect v-model="newModeB" class="add-select" :options="[...(MODES).map((m) => ({ value: m.value, label: modeLabel(m.value) }))]" />
        <button class="btn btn-success" @click="addParam('b')"><Plus :size="15" />{{ t('common.add') }}</button>
      </div>

      <div class="save-row">
        <button class="btn btn-primary" data-save :disabled="!dirtyB" @click="save('b')">{{ t('common.save') }}</button>
        <span v-if="dirtyB" class="unsaved-badge">{{ t('common.unsavedBadge') }}</span>
        <button class="btn btn-ghost" @click="loadChannel('b')"><RotateCcw :size="14" />{{ t('params.undoChanges') }}</button>
      </div>
    </section>

    <div v-if="msg" class="msg-bar" :class="{ err: msgErr }">{{ msg }}</div>
    <p class="hint" style="margin-top:var(--sp-3)">{{ t('params.addHint') }}</p>
  </div>
</template>

<style scoped>
.subtitle { color: var(--text-muted); font-size: var(--text-sm); margin: var(--sp-1) 0 var(--sp-5); }
.channel-section { background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: var(--sp-5); margin-bottom: var(--sp-4); }
.channel-header { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-3); }
.ch-title { font-size: var(--text-lg); margin: 0; }
.ch-title { color: var(--ch-color); }
.dirty-badge { color: var(--warning); font-size: var(--text-xs); }
.settings-row { display: flex; gap: var(--sp-4); margin-bottom: var(--sp-3); }
.setting { flex: 1; }
.setting label { display: block; font-size: var(--text-xs); color: var(--text-muted); margin-bottom: var(--sp-1); }
.setting .app-select, .setting input { width: 100%; }
.readonly-value {
  display: flex; align-items: center; justify-content: space-between;
  padding: var(--sp-3) var(--sp-4);
  border: 1px dashed var(--border); border-radius: var(--radius-md);
  color: var(--text); font-weight: 600; text-decoration: none;
  transition: var(--transition-props);
}
.readonly-value:hover { border-color: var(--border-hover); background: var(--bg-hover); text-decoration: none; }
.readonly-link { font-size: var(--text-xs); font-weight: 500; color: var(--text-secondary); }
table { width: 100%; border-collapse: collapse; margin-bottom: var(--sp-3); }
th { text-align: left; font-size: var(--text-xs); color: var(--text-muted); padding: var(--sp-2) var(--sp-2); border-bottom: 1px solid var(--border-subtle); }
td { padding: var(--sp-2); font-size: var(--text-sm); border-bottom: 1px solid var(--border-subtle); vertical-align: middle; }
.path { font-family: var(--font-mono); color: var(--text-secondary); word-break: break-all; font-size: var(--text-xs); }
.th-prefix { font-family: var(--font-mono); font-weight: 400; color: var(--text-muted); margin-left: var(--sp-2); }
.mode-badge { display: inline-block; padding: 1px 8px; border-radius: 99px; font-size: var(--text-xs); font-weight: 600; }
.mode-distance { background: var(--info-surface); color: var(--info); }
.mode-shock { background: var(--danger-surface); color: var(--danger); }
.mode-touch { background: var(--success-surface); color: var(--success); }
.mode-combo { background: var(--warning-surface); color: var(--warning); }
.mode-boost { background: rgba(var(--accent-rgb), 0.1); color: var(--purple); }
.actions { display: flex; gap: var(--sp-1); }
.editing { background: rgba(var(--accent-rgb), 0.04); }
.disabled-row { opacity: 0.5; }
.path-disabled { text-decoration: line-through; }
.edit-select { width: 100%; min-width: 0; }
.edit-input { padding: var(--sp-1) var(--sp-2); border: 1px solid var(--accent); border-radius: var(--radius-sm); background: var(--bg-elevated); color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); width: 100%; }
.add-form { display: flex; gap: var(--sp-2); align-items: center; margin-bottom: var(--sp-3); }
.prefix-label { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); white-space: nowrap; background: var(--bg-tertiary); padding: var(--sp-2) var(--sp-2); border-radius: var(--radius-sm) 0 0 var(--radius-sm); border: 1px solid var(--border); border-right: none; }
.add-input { flex: 1; padding: var(--sp-2) var(--sp-3); border: 1px solid var(--border); border-radius: 0 var(--radius-md) var(--radius-md) 0; background: var(--bg-elevated); color: var(--text); font-family: var(--font-mono); font-size: var(--text-xs); }
.add-select { width: 120px; min-width: 0; }
.save-row { display: flex; gap: var(--sp-2); align-items: center; }
.msg-bar { text-align: center; padding: var(--sp-2) var(--sp-4); background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-md); font-size: var(--text-sm); color: var(--success); }
.msg-bar.err { color: var(--danger); }
.hint { font-size: var(--text-xs); color: var(--text-muted); }
.empty { color: var(--text-muted); text-align: center; padding: var(--sp-4); }
</style>
