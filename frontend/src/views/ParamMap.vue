<script setup lang="ts">
// Parameter mapping. One list of parameters; each row picks the channels
// (A, B or both) it drives. Stored per channel by the backend, so rows are
// split on save and merged on load (identical entries on both channels
// become one row; different settings per channel stay separate rows).
import AppSelect from '@/components/AppSelect.vue'
import { Check, Pencil, Plus, Trash2, X } from 'lucide-vue-next'
import { computed, reactive, ref, onMounted, onActivated } from 'vue'
import { api } from '@/api'
import { useI18n } from '@/i18n'
import { useUnsaved } from '@/unsaved'
import AutoSaveStatus from '@/components/AutoSaveStatus.vue'
import { toast } from '@/dialog'

const { t } = useI18n()

type Ch = 'a' | 'b'
interface RangeSeg { from: number; to: number; mode: string }
interface Settings {
  path: string; mode: string; enabled: boolean
  // mode 'ranges': value ranges -> modes, crossfaded (see internal/handler/multi.go)
  ranges?: RangeSeg[]; crossfade?: number; blend?: number; crossfade_curve?: string; remap?: boolean
}
interface Row extends Settings { a: boolean; b: boolean }

const MODES = ['distance', 'shock', 'touch', 'combo', 'boost']
const RANGES = 'ranges'
const PREFIX = '/avatar/parameters/'
const CHANNELS: Ch[] = ['a', 'b']

const rows = ref<Row[]>([])
const defaultMode = reactive<Record<Ch, string>>({ a: 'distance', b: 'distance' })
const limits = reactive<Record<Ch, number>>({ a: 0, b: 0 })
const saving = ref(false)
/** Row errors from the server, by row index */
const rowErrors = ref<Record<number, string>>({})

// add form
const newPath = ref('')
const newMode = ref('distance')
const newCh = reactive<Record<Ch, boolean>>({ a: true, b: false })
const addError = ref('')
// inline edit
const editIndex = ref<number | null>(null)
const editPath = ref('')
const editMode = ref('distance')
const editError = ref('')

const modeOptions = computed(() => [...MODES, RANGES].map((m) => ({ value: m, label: modeLabel(m) })))
const defaultModeOptions = computed(() => MODES.map((m) => ({ value: m, label: modeLabel(m) })))
const mixModeOptions = computed(() => MODES.map((m) => ({ value: m, label: modeLabel(m) })))

// Auto-saved: add / remove / toggles / confirmed edits / mode and range
// changes apply live shortly after the change.
const unsaved = useUnsaved(() => ({ rows: rows.value, d: { ...defaultMode } }), { save: () => save(false) })

function modeLabel(mode: string) {
  const key = 'params.mode' + mode.charAt(0).toUpperCase() + mode.slice(1)
  const tr = t(key)
  return tr !== key ? tr : mode
}
function shortPath(path: string) { return path.startsWith(PREFIX) ? path.slice(PREFIX.length) : path }

// ---- ranges helpers
function ensureRanges(p: Settings) {
  if (p.mode !== RANGES) return
  if (!p.ranges?.length) p.ranges = [{ from: 0, to: 0.5, mode: 'distance' }, { from: 0.5, to: 1, mode: 'shock' }]
  p.crossfade ??= 0.5
  p.blend ??= 0.05
  p.crossfade_curve ??= 'equal_power'
  p.remap ??= true
}
function addRange(p: Settings) {
  const rs = p.ranges!
  const last = rs[rs.length - 1]
  if (last && last.to - last.from > 0.1) {
    const mid = Math.round((last.from + last.to) / 2 * 100) / 100
    rs.push({ from: mid, to: last.to, mode: 'shock' })
    last.to = mid
  } else rs.push({ from: last ? last.to : 0, to: 1, mode: 'distance' })
}
function rangeError(p: Settings): string {
  for (const [i, r] of (p.ranges || []).entries()) {
    if (!(r.to > r.from) || r.from < 0 || r.to > 1) return t('params.rangeInvalid', { n: i + 1 })
  }
  return ''
}
function rangeStyle(r: RangeSeg) {
  return { left: (r.from * 100) + '%', width: Math.max(0, (r.to - r.from) * 100) + '%' }
}

// ---- merge / split between rows and per-channel lists
function settingsOf(r: Row): Settings {
  const { a: _a, b: _b, ...s } = r
  return JSON.parse(JSON.stringify(s))
}
function sameSettings(x: Settings, y: Settings) { return JSON.stringify(x) === JSON.stringify(y) }

/** Same rules as the backend; checks path+channel uniqueness. */
function normalize(raw: string, chs: Record<Ch, boolean>, skip: number | null): { path?: string; error?: string } {
  let path = raw.trim()
  if (!path) return { error: t('params.errEmpty') }
  if (!path.startsWith('/')) path = PREFIX + path
  if (/[\s#,?\[\]{}]/.test(path) || path.endsWith('/')) return { error: t('params.errBadPath') }
  for (const [i, r] of rows.value.entries()) {
    if (i !== skip && r.path === path && CHANNELS.some((c) => chs[c] && r[c])) return { error: t('params.paramExistsOn') }
  }
  return { path }
}

async function load() {
  unsaved.markLoading()
  rowErrors.value = {}
  editIndex.value = null
  const out: Row[] = []
  for (const c of CHANNELS) {
    const d = await api(`/api/v1/params/${c}`)
    defaultMode[c] = d.default_mode || 'distance'
    limits[c] = d.strength_limit ?? 0
    for (const p of d.params || []) {
      const s: Settings = {
        ...(typeof p === 'string' ? {} : p),
        path: typeof p === 'string' ? p : p.path,
        mode: (typeof p === 'string' ? '' : p.mode) || d.default_mode || 'distance',
        enabled: typeof p === 'string' ? true : p.enabled !== false,
      }
      // same path with identical settings on A -> one row for both
      const twin = c === 'b' ? out.find((r) => r.a && !r.b && r.path === s.path && sameSettings(settingsOf(r), s)) : undefined
      if (twin) twin.b = true
      else out.push({ ...s, a: c === 'a', b: c === 'b' })
    }
  }
  rows.value = out
  unsaved.markClean()
}

function addParam() {
  if (!newCh.a && !newCh.b) { addError.value = t('params.pickChannel'); return }
  const r = normalize(newPath.value, newCh, null)
  if (r.error) { addError.value = r.error; return }
  const row: Row = { path: r.path!, mode: newMode.value, enabled: true, a: newCh.a, b: newCh.b }
  ensureRanges(row)
  rows.value.push(row)
  newPath.value = ''
  addError.value = ''
}

function removeRow(i: number) {
  rows.value.splice(i, 1)
  if (editIndex.value === i) editIndex.value = null
  else if (editIndex.value !== null && editIndex.value > i) editIndex.value--
  rowErrors.value = {}
}

function toggleChannel(i: number, c: Ch) {
  const r = rows.value[i]
  const other: Ch = c === 'a' ? 'b' : 'a'
  if (r[c] && !r[other]) return // keep at least one channel (delete the row instead)
  if (!r[c] && rows.value.some((x, j) => j !== i && x.path === r.path && x[c])) {
    toast(t('params.paramExistsOn'), 'error')
    return
  }
  r[c] = !r[c]
}

function startEdit(i: number) {
  editIndex.value = i
  editPath.value = shortPath(rows.value[i].path)
  editMode.value = rows.value[i].mode
  editError.value = ''
}
/** Returns false if the edit is invalid (row stays open with an error). */
function confirmEdit(): boolean {
  const i = editIndex.value
  if (i === null) return true
  const row = rows.value[i]
  const r = normalize(editPath.value, { a: row.a, b: row.b }, i)
  if (r.error) { editError.value = r.error; return false }
  rows.value[i] = { ...row, path: r.path!, mode: editMode.value }
  ensureRanges(rows.value[i])
  delete rowErrors.value[i]
  editIndex.value = null
  editError.value = ''
  return true
}
function cancelEdit() { editIndex.value = null; editError.value = '' }

const ERR_KEYS: Record<string, string> = {
  empty_path: 'params.errEmpty', bad_path: 'params.errBadPath', bad_mode: 'params.errBadMode', duplicate: 'params.paramExists',
  bad_ranges: 'params.errBadRanges',
}

async function save(manual = true) {
  // On an explicit save (Ctrl+S) an open edit row is part of what the user
  // expects to save; auto-save leaves it open.
  if (manual && !confirmEdit()) return
  saving.value = true
  rowErrors.value = {}
  // split rows into per-channel lists, remembering which row each came from
  const lists: Record<Ch, Settings[]> = { a: [], b: [] }
  const origin: Record<Ch, number[]> = { a: [], b: [] }
  rows.value.forEach((r, i) => {
    for (const c of CHANNELS) if (r[c]) { lists[c].push(settingsOf(r)); origin[c].push(i) }
  })
  try {
    const body = Object.fromEntries(CHANNELS.map((c) => [c, { params: lists[c], default_mode: defaultMode[c] }]))
    const resp = await fetch('/api/v1/params', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    })
    const data = await resp.json().catch(() => ({}))
    if (resp.ok && data.success) {
      // the backend normalises paths (prefix); apply them back to the rows
      for (const c of CHANNELS) (data[c] || []).forEach((p: any, k: number) => {
        const i = origin[c][k]
        if (i !== undefined && rows.value[i] && p.path) rows.value[i].path = p.path
      })
      unsaved.markClean()
      if (manual) toast(t('params.appliedLive'), 'success')
    } else if (Array.isArray(data.errors)) {
      for (const e of data.errors) {
        const i = origin[e.channel as Ch]?.[e.index]
        if (i !== undefined) rowErrors.value[i] = t(ERR_KEYS[e.code] || 'common.saveFailed') + (e.detail ? `: ${e.detail}` : '')
      }
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

const count = computed(() => ({
  a: rows.value.filter((r) => r.a && r.enabled).length,
  b: rows.value.filter((r) => r.b && r.enabled).length,
  total: rows.value.length,
}))

onMounted(load)

// Kept alive: refresh the read-only limits when returning from the Strength page.
onActivated(async () => {
  try {
    const d = await api('/api/v1/strength_limit')
    limits.a = d.channel_a ?? limits.a
    limits.b = d.channel_b ?? limits.b
  } catch {}
})
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('params.title') }}</h1>
    <p class="page-desc">{{ t('params.desc') }}</p>

    <section class="channel-section">
      <div class="settings-row">
        <div v-for="c in CHANNELS" :key="c" class="setting">
          <label><span class="ch-tag sm" :class="'ch-' + c">{{ c.toUpperCase() }}</span> {{ t('params.defaultMode') }}</label>
          <AppSelect v-model="defaultMode[c]" :options="defaultModeOptions" />
        </div>
        <div v-for="c in CHANNELS" :key="'l' + c" class="setting">
          <label><span class="ch-tag sm" :class="'ch-' + c">{{ c.toUpperCase() }}</span> {{ t('params.strengthLimit') }}</label>
          <router-link to="/strength" class="readonly-value" :title="t('params.strengthLimitHint')">
            <span>{{ limits[c] }}</span><span class="readonly-link">{{ t('params.strengthLimitEdit') }} →</span>
          </router-link>
        </div>
      </div>

      <div class="count-row">
        <span class="count">{{ t('params.countUnified', { a: count.a, b: count.b, total: count.total }) }}</span>
      </div>

      <table>
        <thead>
          <tr>
            <th style="width:44px">{{ t('params.colEnabled') }}</th>
            <th>{{ t('params.colPath') }} <span class="th-prefix">/avatar/parameters/…</span></th>
            <th style="width:90px" class="center">{{ t('params.colChannels') }}</th>
            <th style="width:150px">{{ t('params.colMode') }}</th>
            <th style="width:80px">{{ t('params.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(p, i) in rows" :key="i">
          <tr :class="{ editing: editIndex === i, 'disabled-row': !p.enabled && editIndex !== i, 'error-row': rowErrors[i] }">
            <td><input v-model="p.enabled" type="checkbox" :aria-label="t('params.colEnabled')"></td>
            <template v-if="editIndex === i">
              <td>
                <div class="edit-wrap">
                  <span class="edit-prefix">/avatar/parameters/</span>
                  <input v-model="editPath" class="edit-input" :class="{ invalid: editError }" @keyup.enter="confirmEdit()" @keyup.esc="cancelEdit()">
                </div>
                <div v-if="editError" class="row-error">{{ editError }}</div>
              </td>
            </template>
            <td v-else class="path" :class="{ 'path-disabled': !p.enabled }" :title="p.path" @dblclick="startEdit(i)">
              {{ shortPath(p.path) }}
              <div v-if="rowErrors[i]" class="row-error">{{ rowErrors[i] }}</div>
            </td>
            <td class="center chs">
              <button v-for="c in CHANNELS" :key="c" type="button" class="ch-toggle" :class="['ch-' + c, { on: p[c] }]"
                role="checkbox" :aria-checked="p[c]" :aria-label="t(c === 'a' ? 'common.channelA' : 'common.channelB')"
                :title="t(c === 'a' ? 'common.channelA' : 'common.channelB')" @click="toggleChannel(i, c)">{{ c.toUpperCase() }}</button>
            </td>
            <template v-if="editIndex === i">
              <td><AppSelect v-model="editMode" class="edit-select" :options="modeOptions" /></td>
              <td class="actions">
                <button class="icon-action success" :title="t('common.confirm')" :aria-label="t('common.confirm')" @click="confirmEdit()"><Check :size="15" /></button>
                <button class="icon-action" :title="t('common.cancel')" :aria-label="t('common.cancel')" @click="cancelEdit()"><X :size="15" /></button>
              </td>
            </template>
            <template v-else>
              <td><span class="mode-badge" :class="'mode-' + p.mode">{{ modeLabel(p.mode) }}</span></td>
              <td class="actions">
                <button class="icon-action" :title="t('common.edit')" :aria-label="t('common.edit')" @click="startEdit(i)"><Pencil :size="15" /></button>
                <button class="icon-action danger" :title="t('common.delete')" :aria-label="t('common.delete')" @click="removeRow(i)"><Trash2 :size="15" /></button>
              </td>
            </template>
          </tr>
          <tr v-if="p.mode === RANGES && editIndex !== i && p.ranges" class="ranges-row" :class="{ 'disabled-row': !p.enabled }">
            <td></td>
            <td colspan="4">
              <div class="ranges-bar" role="img" :aria-label="t('params.rangesTitle')">
                <div v-for="(r, ri) in p.ranges" :key="ri" class="ranges-seg" :class="'mode-' + r.mode" :style="rangeStyle(r)">{{ modeLabel(r.mode) }}</div>
              </div>
              <div v-for="(r, ri) in p.ranges" :key="ri" class="range-line">
                <input type="number" v-model.number="r.from" min="0" max="1" step="0.05" class="num" :aria-label="t('params.rangeFrom')">
                <span>–</span>
                <input type="number" v-model.number="r.to" min="0" max="1" step="0.05" class="num" :aria-label="t('params.rangeTo')">
                <AppSelect v-model="r.mode" class="range-mode" :options="mixModeOptions" />
                <button class="icon-action danger" :disabled="p.ranges.length <= 1" :title="t('common.delete')" :aria-label="t('common.delete')" @click="p.ranges.splice(ri, 1)"><Trash2 :size="14" /></button>
              </div>
              <button class="btn btn-ghost btn-sm" @click="addRange(p)"><Plus :size="14" />{{ t('params.rangeAdd') }}</button>
              <div class="range-opts">
                <label>{{ t('params.crossfade') }}
                  <input type="number" v-model.number="p.crossfade" min="0" max="10" step="0.1" class="num">s</label>
                <label :title="t('params.blendHint')">{{ t('params.blend') }}
                  <input type="number" v-model.number="p.blend" min="0" max="0.5" step="0.01" class="num"></label>
                <label>{{ t('params.curve') }}
                  <AppSelect v-model="p.crossfade_curve" class="range-mode"
                    :options="[{ value: 'equal_power', label: t('params.curveEqualPower') }, { value: 'linear', label: t('params.curveLinear') }]" /></label>
                <label class="chk" :title="t('params.remapHint')"><input type="checkbox" v-model="p.remap">{{ t('params.remap') }}</label>
              </div>
              <div v-if="rangeError(p)" class="row-error">{{ rangeError(p) }}</div>
              <p class="range-hint">{{ t('params.rangesHint') }}</p>
            </td>
          </tr>
          </template>
          <tr v-if="!rows.length"><td colspan="5" class="empty">{{ t('params.noParams') }}</td></tr>
        </tbody>
      </table>

      <div class="add-form" data-tour="param-add">
        <div class="add-path" :class="{ invalid: addError }">
          <span class="prefix-label">/avatar/parameters/</span>
          <input v-model="newPath" placeholder="pcs/contact/enterPass" class="add-input" @input="addError = ''" @keyup.enter="addParam()">
        </div>
        <span class="chs">
          <button v-for="c in CHANNELS" :key="c" type="button" class="ch-toggle" :class="['ch-' + c, { on: newCh[c] }]"
            role="checkbox" :aria-checked="newCh[c]" :aria-label="t(c === 'a' ? 'common.channelA' : 'common.channelB')"
            @click="newCh[c] = !newCh[c]; addError = ''">{{ c.toUpperCase() }}</button>
        </span>
        <AppSelect v-model="newMode" class="add-select" data-tour="param-mode-select" :options="modeOptions" />
        <button class="btn btn-ghost" @click="addParam()"><Plus :size="15" />{{ t('common.add') }}</button>
      </div>
      <div v-if="addError" class="row-error add-error">{{ addError }}</div>
    </section>

    <p class="hint">{{ t('params.addHint') }}</p>

    <div class="save-bar sticky">
      <span data-tour="param-save"><AutoSaveStatus :state="unsaved.state.value" @retry="unsaved.flush()" /></span>
      <span v-if="saving" class="save-hint">…</span>
      <span class="save-hint">{{ t('params.liveHint') }}</span>
    </div>
  </div>
</template>

<style scoped>
.ranges-row td { padding-top: 0; }
.ranges-bar { position: relative; height: 18px; border-radius: var(--radius-sm); background: var(--bg-inset); margin: 4px 0 8px; overflow: hidden; }
.ranges-seg { position: absolute; top: 0; height: 100%; font-size: var(--text-2xs); line-height: 18px; text-align: center; color: var(--text); background: rgba(var(--accent-rgb), 0.18); border-right: 1px solid var(--bg-card); overflow: hidden; white-space: nowrap; }
.ranges-seg.mode-shock { background: rgba(var(--red-rgb), 0.25); }
.ranges-seg.mode-touch { background: rgba(var(--green-rgb), 0.25); }
.ranges-seg.mode-combo { background: rgba(var(--yellow-rgb), 0.3); }
.ranges-seg.mode-boost { background: rgba(var(--accent-rgb), 0.35); }
.range-line { display: flex; align-items: center; gap: var(--sp-2); margin-bottom: 4px; font-size: var(--text-sm); }
.range-line .num, .range-opts .num { width: 64px; text-align: center; }
.range-mode { width: 130px; min-width: 0; }
.range-opts { display: flex; flex-wrap: wrap; align-items: center; gap: var(--sp-3); margin-top: var(--sp-2); font-size: var(--text-sm); color: var(--text-secondary); }
.range-opts label { display: inline-flex; align-items: center; gap: 4px; }
.range-hint { font-size: var(--text-xs); color: var(--text-muted); margin: 4px 0 0; }
.channel-section { background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: var(--sp-5); margin-bottom: var(--sp-4); }
.channel-header { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-3); }
.ch-title { display: flex; align-items: center; gap: var(--sp-2); font-size: var(--text-lg); margin: 0; }
.count { margin-left: auto; font-size: var(--text-xs); color: var(--text-muted); }
.count-row { display: flex; margin-bottom: var(--sp-2); }
.settings-row { flex-wrap: wrap; }
.setting { min-width: 160px; }
.center { text-align: center; }
.chs { white-space: nowrap; }
.ch-toggle { width: 26px; height: 22px; margin: 0 2px; border: 1px solid var(--border); border-radius: 6px; background: transparent; color: var(--text-muted); font-size: var(--text-xs); font-weight: 700; cursor: pointer; transition: var(--transition-props); }
.ch-toggle:hover { border-color: var(--border-hover); color: var(--text); }
.ch-toggle.on.ch-a { background: var(--ch-a-color); border-color: var(--ch-a-color); color: var(--bg-base, #1e1e2e); }
.ch-toggle.on.ch-b { background: var(--ch-b-color); border-color: var(--ch-b-color); color: var(--bg-base, #1e1e2e); }
.setting label .ch-tag { vertical-align: middle; }
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
.mode-ranges { background: rgba(var(--yellow-rgb), 0.15); color: var(--warning); }
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
