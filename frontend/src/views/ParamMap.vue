<script setup lang="ts">
import AppSelect from '@/components/AppSelect.vue'
import { Check, Pencil, Plus, RotateCcw, Trash2, X } from 'lucide-vue-next'
import { computed, reactive, ref, onMounted, onActivated } from 'vue'
import { api } from '@/api'
import { useI18n } from '@/i18n'
import { useUnsaved } from '@/unsaved'
import { toast } from '@/dialog'

const { t } = useI18n()

type Ch = 'a' | 'b'
interface Param { path: string; mode: string; enabled: boolean }
interface ChannelState {
  params: Param[]
  defaultMode: string
  limit: number
  // add form
  newPath: string
  newMode: string
  // inline edit (per channel, so A and B never clobber each other)
  editIndex: number | null
  editPath: string
  editMode: string
  editError: string
  addError: string
}

const MODES = ['distance', 'shock', 'touch', 'combo', 'boost']
const PREFIX = '/avatar/parameters/'
const CHANNELS: Ch[] = ['a', 'b']

function emptyChannel(): ChannelState {
  return { params: [], defaultMode: 'distance', limit: 0, newPath: '', newMode: 'distance',
    editIndex: null, editPath: '', editMode: 'distance', editError: '', addError: '' }
}
const st = reactive<Record<Ch, ChannelState>>({ a: emptyChannel(), b: emptyChannel() })
const saving = ref(false)
/** Row errors from the server: key "a:2" -> message */
const rowErrors = ref<Record<string, string>>({})

const modeOptions = computed(() => MODES.map((m) => ({ value: m, label: modeLabel(m) })))

// Dirty = differs from what was last loaded/saved (not "was touched").
const unsaved = useUnsaved(() => CHANNELS.map((c) => ({ p: st[c].params, m: st[c].defaultMode })))

function modeLabel(mode: string) {
  const key = 'params.mode' + mode.charAt(0).toUpperCase() + mode.slice(1)
  const tr = t(key)
  return tr !== key ? tr : mode
}
function shortPath(path: string) { return path.startsWith(PREFIX) ? path.slice(PREFIX.length) : path }

/** Same rules as the backend; returns full path or an error message. */
function normalize(c: Ch, raw: string, skipIndex: number | null): { path?: string; error?: string } {
  let path = raw.trim()
  if (!path) return { error: t('params.errEmpty') }
  if (!path.startsWith('/')) path = PREFIX + path
  if (/[\s#,?\[\]{}]/.test(path) || path.endsWith('/')) return { error: t('params.errBadPath') }
  if (st[c].params.some((p, i) => i !== skipIndex && p.path === path)) return { error: t('params.paramExists') }
  return { path }
}

async function load() {
  rowErrors.value = {}
  for (const c of CHANNELS) {
    const d = await api(`/api/v1/params/${c}`)
    Object.assign(st[c], emptyChannel(), {
      params: (d.params || []).map((p: any) => ({
        path: typeof p === 'string' ? p : p.path,
        mode: (typeof p === 'string' ? '' : p.mode) || d.default_mode || 'distance',
        enabled: typeof p === 'string' ? true : p.enabled !== false,
      })),
      defaultMode: d.default_mode || 'distance',
      newMode: d.default_mode || 'distance',
      limit: d.strength_limit ?? 0,
    })
  }
  unsaved.markClean()
}

function addParam(c: Ch) {
  const s = st[c]
  const r = normalize(c, s.newPath, null)
  if (r.error) { s.addError = r.error; return }
  s.params.push({ path: r.path!, mode: s.newMode, enabled: true })
  s.newPath = ''
  s.addError = ''
}

function removeParam(c: Ch, i: number) {
  const s = st[c]
  s.params.splice(i, 1)
  if (s.editIndex === i) s.editIndex = null
  else if (s.editIndex !== null && s.editIndex > i) s.editIndex--
  rowErrors.value = {}
}

function startEdit(c: Ch, i: number) {
  const s = st[c]
  s.editIndex = i
  s.editPath = shortPath(s.params[i].path)
  s.editMode = s.params[i].mode
  s.editError = ''
}

/** Returns false if the edit is invalid (row stays open with an error). */
function confirmEdit(c: Ch): boolean {
  const s = st[c]
  if (s.editIndex === null) return true
  const r = normalize(c, s.editPath, s.editIndex)
  if (r.error) { s.editError = r.error; return false }
  s.params[s.editIndex] = { ...s.params[s.editIndex], path: r.path!, mode: s.editMode }
  delete rowErrors.value[`${c}:${s.editIndex}`]
  s.editIndex = null
  s.editError = ''
  return true
}
function cancelEdit(c: Ch) { st[c].editIndex = null; st[c].editError = '' }

const ERR_KEYS: Record<string, string> = {
  empty_path: 'params.errEmpty', bad_path: 'params.errBadPath', bad_mode: 'params.errBadMode', duplicate: 'params.paramExists',
}

async function save() {
  // An open edit row is part of what the user expects to save.
  for (const c of CHANNELS) if (!confirmEdit(c)) return
  saving.value = true
  rowErrors.value = {}
  try {
    const body = Object.fromEntries(CHANNELS.map((c) => [c, { params: st[c].params, default_mode: st[c].defaultMode }]))
    const resp = await fetch('/api/v1/params', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    })
    const data = await resp.json().catch(() => ({}))
    if (resp.ok && data.success) {
      for (const c of CHANNELS) if (data[c]) st[c].params = data[c]
      unsaved.markClean()
      toast(t('params.appliedLive'), 'success')
    } else if (Array.isArray(data.errors)) {
      for (const e of data.errors) rowErrors.value[`${e.channel}:${e.index}`] = t(ERR_KEYS[e.code] || 'common.saveFailed')
      toast(t('params.fixErrors', { n: data.errors.length }), 'error')
    } else {
      toast(data.detail || data.error || t('common.saveFailed'), 'error')
    }
  } catch {
    toast(t('common.saveFailed'), 'error')
  } finally {
    saving.value = false
  }
}

onMounted(load)

// Kept alive: refresh the read-only limits when returning from the Strength page.
onActivated(async () => {
  try {
    const d = await api('/api/v1/strength_limit')
    st.a.limit = d.channel_a ?? st.a.limit
    st.b.limit = d.channel_b ?? st.b.limit
  } catch {}
})
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('params.title') }}</h1>
    <p class="page-desc">{{ t('params.desc') }}</p>

    <section v-for="c in CHANNELS" :key="c" class="channel-section" :class="'ch-' + c">
      <div class="channel-header">
        <h2 class="ch-title"><span class="ch-tag">{{ c.toUpperCase() }}</span>{{ t(c === 'a' ? 'common.channelA' : 'common.channelB') }}</h2>
        <span class="count">{{ t('params.enabledCount', { enabled: st[c].params.filter(p => p.enabled).length, total: st[c].params.length }) }}</span>
      </div>

      <div class="settings-row">
        <div class="setting">
          <label>{{ t('params.defaultMode') }}</label>
          <AppSelect v-model="st[c].defaultMode" :options="modeOptions" />
        </div>
        <div class="setting">
          <label>{{ t('params.strengthLimit') }}</label>
          <router-link to="/strength" class="readonly-value" :title="t('params.strengthLimitHint')">
            <span>{{ st[c].limit }}</span><span class="readonly-link">{{ t('params.strengthLimitEdit') }} →</span>
          </router-link>
        </div>
      </div>

      <table>
        <thead>
          <tr>
            <th style="width:44px">{{ t('params.colEnabled') }}</th>
            <th>{{ t('params.colPath') }} <span class="th-prefix">/avatar/parameters/…</span></th>
            <th style="width:150px">{{ t('params.colMode') }}</th>
            <th style="width:80px">{{ t('params.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(p, i) in st[c].params" :key="p.path + i"
            :class="{ editing: st[c].editIndex === i, 'disabled-row': !p.enabled && st[c].editIndex !== i, 'error-row': rowErrors[c + ':' + i] }"
          >
            <td><input v-model="p.enabled" type="checkbox" :aria-label="t('params.colEnabled')"></td>
            <template v-if="st[c].editIndex === i">
              <td>
                <div class="edit-wrap">
                  <span class="edit-prefix">/avatar/parameters/</span>
                  <input
                    v-model="st[c].editPath" class="edit-input" :class="{ invalid: st[c].editError }"
                    @keyup.enter="confirmEdit(c)" @keyup.esc="cancelEdit(c)"
                  >
                </div>
                <div v-if="st[c].editError" class="row-error">{{ st[c].editError }}</div>
              </td>
              <td><AppSelect v-model="st[c].editMode" class="edit-select" :options="modeOptions" /></td>
              <td class="actions">
                <button class="icon-action success" :title="t('common.confirm')" :aria-label="t('common.confirm')" @click="confirmEdit(c)"><Check :size="15" /></button>
                <button class="icon-action" :title="t('common.cancel')" :aria-label="t('common.cancel')" @click="cancelEdit(c)"><X :size="15" /></button>
              </td>
            </template>
            <template v-else>
              <td class="path" :class="{ 'path-disabled': !p.enabled }" :title="p.path" @dblclick="startEdit(c, i)">
                {{ shortPath(p.path) }}
                <div v-if="rowErrors[c + ':' + i]" class="row-error">{{ rowErrors[c + ':' + i] }}</div>
              </td>
              <td><span class="mode-badge" :class="'mode-' + p.mode">{{ modeLabel(p.mode) }}</span></td>
              <td class="actions">
                <button class="icon-action" :title="t('common.edit')" :aria-label="t('common.edit')" @click="startEdit(c, i)"><Pencil :size="15" /></button>
                <button class="icon-action danger" :title="t('common.delete')" :aria-label="t('common.delete')" @click="removeParam(c, i)"><Trash2 :size="15" /></button>
              </td>
            </template>
          </tr>
          <tr v-if="!st[c].params.length"><td colspan="4" class="empty">{{ t('params.noParams') }}</td></tr>
        </tbody>
      </table>

      <div class="add-form" :data-tour="c === 'a' ? 'param-add' : undefined">
        <div class="add-path" :class="{ invalid: st[c].addError }">
          <span class="prefix-label">/avatar/parameters/</span>
          <input
            v-model="st[c].newPath" placeholder="pcs/contact/enterPass" class="add-input"
            @input="st[c].addError = ''" @keyup.enter="addParam(c)"
          >
        </div>
        <AppSelect v-model="st[c].newMode" class="add-select" :data-tour="c === 'a' ? 'param-mode-select' : undefined" :options="modeOptions" />
        <button class="btn btn-ghost" @click="addParam(c)"><Plus :size="15" />{{ t('common.add') }}</button>
      </div>
      <div v-if="st[c].addError" class="row-error add-error">{{ st[c].addError }}</div>
    </section>

    <p class="hint">{{ t('params.addHint') }}</p>

    <div class="save-bar sticky">
      <button class="btn btn-primary" data-tour="param-save" :disabled="saving || !unsaved.dirty.value" @click="save">
        {{ saving ? t('params.saving') : t('common.save') }}
      </button>
      <button class="btn btn-ghost" :disabled="saving || !unsaved.dirty.value" @click="load">
        <RotateCcw :size="14" />{{ t('params.undoChanges') }}
      </button>
      <span v-if="unsaved.dirty.value" class="unsaved-badge">{{ t('common.unsavedBadge') }}</span>
      <span class="save-hint">{{ t('params.liveHint') }}</span>
    </div>
  </div>
</template>

<style scoped>
.channel-section { background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: var(--sp-5); margin-bottom: var(--sp-4); }
.channel-header { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-3); }
.ch-title { display: flex; align-items: center; gap: var(--sp-2); font-size: var(--text-lg); margin: 0; }
.count { margin-left: auto; font-size: var(--text-xs); color: var(--text-muted); }
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
.edit-wrap { display: flex; align-items: stretch; }
.edit-prefix { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); background: var(--bg-tertiary); padding: var(--sp-1) var(--sp-2); border: 1px solid var(--accent); border-right: none; border-radius: var(--radius-sm) 0 0 var(--radius-sm); display: flex; align-items: center; white-space: nowrap; }
.edit-wrap .edit-input { border-radius: 0 var(--radius-sm) var(--radius-sm) 0; }
.edit-input.invalid, .add-path.invalid .add-input, .add-path.invalid .prefix-label { border-color: var(--danger); }
.add-path { flex: 1; display: flex; min-width: 0; }
.add-path .add-input { flex: 1; min-width: 0; }
.row-error { margin-top: 4px; font-family: var(--font); font-size: var(--text-xs); color: var(--danger); }
.add-error { margin: calc(-1 * var(--sp-2)) 0 var(--sp-2); }
.error-row { background: var(--danger-surface); }
.path { cursor: text; }
.save-hint { margin-left: auto; font-size: var(--text-xs); color: var(--text-muted); }
</style>
