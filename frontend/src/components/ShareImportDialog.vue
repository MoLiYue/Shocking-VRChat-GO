<script setup lang="ts">
// Import: preview a dropped / picked share (or pasted code), choose what to
// apply, then apply (the current setup is backed up as a profile first).
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { X, Upload } from 'lucide-vue-next'
import { useI18n } from '@/i18n'
import { toast } from '@/dialog'
import { shareState, previewShare, renderShareCard } from '@/share'

const { t } = useI18n()
const sections = reactive({ params: true, mode_config: true, curves: true, overlimit: true, presets: true })
const channels = reactive({ a: true, b: true })
const mode = ref<'merge' | 'replace'>('merge')
const enableRules = ref(false)
const busy = ref(false)
const cardUrl = ref('')
const pasted = ref('')
const fileRef = ref<HTMLInputElement | null>(null)

const pv = computed(() => shareState.preview)
const PREFIX = '/avatar/parameters/'
const short = (p: string) => (p.startsWith(PREFIX) ? p.slice(PREFIX.length) : p)
function modeLabel(m: string) {
  const key = 'params.mode' + m.charAt(0).toUpperCase() + m.slice(1)
  const tr = t(key)
  return tr !== key ? tr : m
}
const errText = computed(() => {
  const e = shareState.error
  if (!e) return ''
  const k = 'share.err_' + e
  const tr = t(k)
  return tr !== k ? tr : t('share.err_invalid') + ` (${e})`
})

watch(pv, async (p) => {
  if (cardUrl.value) { URL.revokeObjectURL(cardUrl.value); cardUrl.value = '' }
  if (!p?.share) return
  Object.assign(sections, { params: true, mode_config: true, curves: true, overlimit: true, presets: true })
  Object.assign(channels, { a: true, b: true })
  mode.value = 'merge'; enableRules.value = false
  try { cardUrl.value = URL.createObjectURL(await renderShareCard(p.share, t, modeLabel)) } catch {}
})
onUnmounted(() => { if (cardUrl.value) URL.revokeObjectURL(cardUrl.value) })

function close() { shareState.importOpen = false; shareState.preview = null; shareState.error = ''; pasted.value = '' }
function pick() { fileRef.value?.click() }
function onPick(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (f) previewShare(f)
  ;(e.target as HTMLInputElement).value = ''
}

async function apply() {
  busy.value = true
  try {
    const resp = await fetch('/api/v1/share/apply', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ share: pv.value.share, sections, channels, mode: mode.value, enable_rules: enableRules.value }),
    })
    const data = await resp.json().catch(() => ({}))
    if (!resp.ok || !data.success) {
      toast(data.detail || t('share.failed'), 'error')
      return
    }
    toast(t('share.applied', { backup: data.backup }), 'success')
    close()
    // Every page caches what it loaded: reload so they all show the import.
    setTimeout(() => window.location.reload(), 900)
  } finally { busy.value = false }
}
const statusLabel = (s: string) => t('share.status_' + s)
</script>

<template>
  <div v-if="shareState.importOpen" class="dlg-backdrop" @mousedown.self="close">
    <div class="dlg share-dlg" role="dialog" aria-modal="true" :aria-label="t('share.importTitle')">
      <div class="head">
        <h3>{{ t('share.importTitle') }}</h3>
        <button class="icon-action" :aria-label="t('common.cancel')" @click="close"><X :size="16" /></button>
      </div>

      <!-- no preview yet: pick a file or paste a code -->
      <template v-if="!pv">
        <p v-if="errText" class="err">{{ errText }}</p>
        <div class="pick">
          <button class="btn btn-primary" @click="pick"><Upload :size="15" />{{ t('share.pickFile') }}</button>
          <span class="hint">{{ t('share.dropHint') }}</span>
          <input ref="fileRef" type="file" accept=".png,.svrc,.json,.txt" hidden @change="onPick">
        </div>
        <textarea v-model="pasted" class="code" :placeholder="t('share.pasteCode')"></textarea>
        <div class="actions"><button class="btn btn-ghost" :disabled="!pasted.trim()" @click="previewShare(pasted)">{{ t('share.readCode') }}</button></div>
      </template>

      <template v-else>
        <div class="meta">
          <strong>{{ pv.meta.title || t('share.cardDefaultTitle') }}</strong>
          <span v-if="pv.meta.avatar">· {{ pv.meta.avatar }}</span>
          <span class="muted">· v{{ pv.meta.app }} · {{ new Date(pv.meta.created).toLocaleString() }}</span>
        </div>
        <p v-if="pv.meta.note" class="note">{{ pv.meta.note }}</p>
        <p v-for="w in pv.warnings" :key="w" class="warn">{{ t('share.warn_' + w) }}</p>
        <div class="card-preview"><img v-if="cardUrl" :src="cardUrl" alt=""></div>

        <div class="grid">
          <section v-for="ch in (['a', 'b'] as const)" :key="ch" class="box">
            <label class="chk"><input type="checkbox" v-model="channels[ch]"><b>{{ t(ch === 'a' ? 'common.channelA' : 'common.channelB') }}</b></label>
            <div v-for="p in pv.channels[ch].params" :key="p.path" class="row">
              <span class="st" :class="p.status">{{ statusLabel(p.status) }}</span>
              <span class="mono">{{ short(p.path) }}</span>
              <span class="muted">{{ modeLabel(p.mode) }}</span>
            </div>
            <div v-if="!pv.channels[ch].params.length" class="muted">—</div>
            <div v-if="mode === 'replace' && pv.channels[ch].removed_on_replace.length" class="removed">
              {{ t('share.willRemove') }}: <span class="mono">{{ pv.channels[ch].removed_on_replace.map(short).join(', ') }}</span>
            </div>
          </section>
        </div>

        <div class="opts">
          <label class="chk"><input type="radio" value="merge" v-model="mode">{{ t('share.modeMerge') }}</label>
          <label class="chk"><input type="radio" value="replace" v-model="mode">{{ t('share.modeReplace') }}</label>
        </div>
        <div class="opts">
          <label class="chk"><input type="checkbox" v-model="sections.params">{{ t('share.secParams') }}</label>
          <label class="chk"><input type="checkbox" v-model="sections.mode_config">{{ t('share.secModes') }}</label>
          <label class="chk"><input type="checkbox" v-model="sections.curves">{{ t('share.secCurves', { n: pv.curves.length }) }}</label>
          <label class="chk"><input type="checkbox" v-model="sections.presets">{{ t('share.secPresets', { n: pv.presets.filter((p: any) => p.state === 'new').length }) }}</label>
          <label class="chk"><input type="checkbox" v-model="sections.overlimit">{{ t('share.secRules', { n: pv.overlimit_rules.length }) }}</label>
        </div>
        <div v-if="sections.overlimit && pv.overlimit_rules.length" class="box">
          <div v-for="(r, i) in pv.overlimit_rules" :key="i" class="row" :class="{ high: r.high }">
            <span>{{ r.name }}</span>
            <span class="mono muted">{{ short(r.condition?.param || '') }} {{ r.condition?.operator }} {{ r.condition?.value }}</span>
            <span>→ {{ r.channel === 'both' ? 'A+B' : String(r.channel).toUpperCase() }} +{{ r.limit_boost }}{{ r.boost_unit === 'percent' ? '%' : '' }}</span>
            <span v-if="r.high" class="warn-tag">{{ t('share.highBoost') }}</span>
          </div>
          <label class="chk"><input type="checkbox" v-model="enableRules">{{ t('share.enableRules') }}</label>
        </div>
        <p class="hint">{{ t('share.limitsHint', { a: pv.reference?.strength_limit?.a ?? '-', b: pv.reference?.strength_limit?.b ?? '-', ca: pv.current_limits.a, cb: pv.current_limits.b }) }}</p>
        <div class="actions">
          <button class="btn btn-ghost" @click="close">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="busy || (!channels.a && !channels.b)" @click="apply">{{ t('share.apply') }}</button>
        </div>
        <p class="hint">{{ t('share.backupHint') }}</p>
      </template>
    </div>
  </div>
</template>

<style scoped>
.dlg-backdrop { position: fixed; inset: 0; z-index: 900; display: grid; place-items: center; background: var(--overlay); }
.share-dlg { width: min(820px, calc(100vw - 32px)); max-height: calc(100vh - 48px); overflow: auto; background: var(--bg-popover); border: 1px solid var(--border-hover); border-radius: var(--radius-lg); padding: var(--sp-5); box-shadow: var(--shadow-pop); }
.head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--sp-3); }
.head h3 { margin: 0; font-size: var(--text-lg); }
.pick { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-3); }
.code { width: 100%; height: 90px; font-family: var(--font-mono); font-size: var(--text-xs); }
.meta { font-size: var(--text-sm); display: flex; flex-wrap: wrap; gap: 6px; }
.note { font-size: var(--text-sm); color: var(--text-secondary); margin: 4px 0; }
.muted { color: var(--text-muted); }
.mono { font-family: var(--font-mono); }
.err, .warn { color: var(--danger); font-size: var(--text-sm); margin: 4px 0; }
.warn { color: var(--warning); }
.card-preview { margin: var(--sp-3) 0; border-radius: var(--radius-md); overflow: hidden; background: var(--bg-inset); max-height: 300px; overflow-y: auto; }
.card-preview img { display: block; width: 100%; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-3); }
.box { border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--sp-3); font-size: var(--text-xs); margin-bottom: var(--sp-2); }
.row { display: flex; gap: var(--sp-2); align-items: center; padding: 2px 0; flex-wrap: wrap; }
.row.high { color: var(--warning); }
.st { font-size: var(--text-2xs); padding: 0 6px; border-radius: var(--radius-full); background: var(--bg-tertiary); }
.st.new { background: var(--success-surface); color: var(--success); }
.st.changed { background: var(--warning-surface); color: var(--warning); }
.removed { margin-top: 6px; color: var(--danger); }
.warn-tag { font-size: var(--text-2xs); color: var(--warning); border: 1px solid var(--warning); border-radius: var(--radius-full); padding: 0 6px; }
.opts { display: flex; flex-wrap: wrap; gap: var(--sp-4); margin: var(--sp-2) 0; font-size: var(--text-sm); }
.chk { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin: 4px 0; }
.actions { display: flex; justify-content: flex-end; gap: var(--sp-2); margin-top: var(--sp-3); }
@media (max-width: 700px) { .grid { grid-template-columns: 1fr; } }
</style>
