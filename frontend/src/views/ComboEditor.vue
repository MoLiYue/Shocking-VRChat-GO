<script setup lang="ts">
import AppSelect from '@/components/AppSelect.vue'
import { useUnsaved } from '@/unsaved'
import { ref, onMounted } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'

const { t } = useI18n()

const activeChannel = ref<'a' | 'b'>('a')
const switchDuration = ref(0.3)
const shockDuration = ref(2)
const shockPreset = ref('')
const shockScale = ref(1.0)
const touchPreset = ref('')
const touchScale = ref(0.35)
const touchDerivative = ref(1)
const triggerBottom = ref(0)
const triggerTop = ref(1)
const presets = ref<string[]>([])
const msg = ref('')

async function loadPresets() {
  const data = await api('/api/v1/wave_presets')
  presets.value = data.presets || []
}

const unsaved = useUnsaved(() => ({ ch: activeChannel.value, switchDuration: switchDuration.value, shockDuration: shockDuration.value, shockPreset: shockPreset.value, shockScale: shockScale.value, touchPreset: touchPreset.value, touchScale: touchScale.value, touchDerivative: touchDerivative.value, triggerBottom: triggerBottom.value, triggerTop: triggerTop.value }))

async function loadCombo() {
  const data = await api(`/api/v1/combo/${activeChannel.value}`)
  switchDuration.value = data.combo?.switch_duration ?? 0.3
  shockDuration.value = data.shock?.duration ?? 2
  shockPreset.value = data.shock?.wave_preset || ''
  shockScale.value = data.shock?.wave_scale ?? 1.0
  touchPreset.value = data.touch?.wave_preset || ''
  touchScale.value = data.touch?.wave_scale ?? 0.35
  touchDerivative.value = data.touch?.n_derivative ?? 1
  triggerBottom.value = data.trigger_range?.bottom ?? 0
  triggerTop.value = data.trigger_range?.top ?? 1
  unsaved.markClean()
}

async function saveCombo() {
  const data = await apiPost(`/api/v1/combo/${activeChannel.value}`, {
    combo: { switch_duration: switchDuration.value },
    shock: { duration: shockDuration.value, wave_preset: shockPreset.value || null, wave_scale: shockScale.value },
    touch: { wave_preset: touchPreset.value || null, wave_scale: touchScale.value, n_derivative: touchDerivative.value },
    trigger_range: { bottom: triggerBottom.value, top: triggerTop.value },
  })
  msg.value = data.success ? t('common.saved') : t('common.saveFailed')
  setTimeout(() => msg.value = '', 3000)
  if (data.success) unsaved.markClean()
}

async function switchChannel(ch: 'a' | 'b') {
  if (ch === activeChannel.value || !(await unsaved.confirmDiscard())) return
  activeChannel.value = ch; loadCombo()
}

onMounted(() => { loadPresets(); loadCombo() })
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('combo.title') }}</h1>
    <p class="page-desc">{{ t('combo.desc') }}</p>

    <div class="ch-tabs">
      <button :class="{active: activeChannel === 'a'}" @click="switchChannel('a')">{{ t('common.channelA') }}</button>
      <button :class="{active: activeChannel === 'b'}" @click="switchChannel('b')">{{ t('common.channelB') }}</button>
    </div>

    <div class="grid">
      <div class="card">
        <h2>{{ t('combo.switchDuration') }}</h2>
        <div class="field">
          <label>{{ t('combo.switchDuration') }}: {{ switchDuration.toFixed(2) }}s</label>
          <input type="range" v-model.number="switchDuration" min="0.1" max="1.5" step="0.05">
        </div>

        <h2>{{ t('combo.shockParams') }}</h2>
        <div class="field">
          <label>{{ t('combo.shockDuration') }}: {{ shockDuration.toFixed(1) }}s</label>
          <input type="range" v-model.number="shockDuration" min="0.5" max="5" step="0.1">
        </div>
        <div class="field">
          <label>{{ t('common.wavePreset') }}</label>
          <AppSelect v-model="shockPreset" :options="[{ value: '', label: t('common.default') }, ...(presets).map((p) => ({ value: p, label: p }))]" />
        </div>
        <div class="field">
          <label>{{ t('common.waveScale') }}: {{ shockScale.toFixed(2) }}</label>
          <input type="range" v-model.number="shockScale" min="0" max="1" step="0.05">
        </div>

        <h2>{{ t('combo.touchParams') }}</h2>
        <div class="field">
          <label>{{ t('common.wavePreset') }}</label>
          <AppSelect v-model="touchPreset" :options="[{ value: '', label: t('common.default') }, ...(presets).map((p) => ({ value: p, label: p }))]" />
        </div>
        <div class="field">
          <label>{{ t('common.waveScale') }}: {{ touchScale.toFixed(2) }}</label>
          <input type="range" v-model.number="touchScale" min="0" max="1" step="0.05">
        </div>
        <div class="field">
          <label>{{ t('combo.derivative') }}</label>
          <AppSelect v-model="touchDerivative" :options="[{ value: 0, label: t('combo.deriv0') }, { value: 1, label: t('combo.deriv1') }, { value: 2, label: t('combo.deriv2') }, { value: 3, label: t('combo.deriv3') }]" />
        </div>

        <div class="save-bar">
          <button class="btn btn-primary" @click="saveCombo">{{ t('common.save') }}</button>
          <button class="btn btn-ghost" @click="loadCombo">{{ t('common.reload') }}</button>
          <span v-if="unsaved.dirty.value" class="unsaved-badge">{{ t('common.unsavedBadge') }}</span>
      <span class="msg" v-if="msg">{{ msg }}</span>
        </div>
      </div>

      <div class="card">
        <h2>{{ t('combo.behavior') }}</h2>
        <div class="diagram">
          <div class="phase phase-shock" style="white-space:pre-line">{{ t('combo.shockPhase') }}</div>
          <div class="divider">{{ switchDuration.toFixed(2) }}s</div>
          <div class="phase phase-touch" style="white-space:pre-line">{{ t('combo.touchPhase') }}</div>
        </div>
        <h2>{{ t('common.triggerRange') }}</h2>
        <div class="field">
          <label>{{ t('common.triggerBottom') }}: {{ triggerBottom.toFixed(2) }}</label>
          <input type="range" v-model.number="triggerBottom" min="0" max="0.5" step="0.01">
        </div>
        <div class="field">
          <label>{{ t('common.triggerTop') }}: {{ triggerTop.toFixed(2) }}</label>
          <input type="range" v-model.number="triggerTop" min="0.3" max="1" step="0.01">
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ch-tabs { display: flex; gap: var(--sp-2); margin-bottom: var(--sp-4); }
.ch-tabs button { padding: var(--sp-2) var(--sp-4); border: 1px solid var(--border); border-radius: var(--radius-md); background: transparent; color: var(--text-muted); cursor: pointer; font-size: var(--text-sm); transition: var(--transition-props); }
.ch-tabs button.active { border-color: var(--accent); color: var(--accent); background: rgba(var(--accent-rgb), 0.08); }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-4); }
.field { margin-bottom: var(--sp-4); }
.field label:not(.toggle-row) { display: block; font-size: var(--text-sm); color: var(--text-label); margin-bottom: var(--sp-1); font-weight: 500; }
.field .app-select { width: 100%; }
.field input[type="range"] { width: 100%; accent-color: var(--accent); }
.diagram { display: flex; align-items: stretch; height: 100px; gap: 2px; margin-bottom: var(--sp-4); }
.phase { flex: 1; display: flex; align-items: center; justify-content: center; border-radius: var(--radius-md); text-align: center; font-size: var(--text-sm); line-height: 1.4; }
.phase-shock { background: rgba(var(--red-rgb), 0.08); border: 1px solid rgba(var(--red-rgb), 0.25); color: var(--danger); }
.phase-touch { background: rgba(var(--green-rgb), 0.08); border: 1px solid rgba(var(--green-rgb), 0.25); color: var(--success); flex: 2; }
.divider { display: flex; align-items: center; padding: 0 var(--sp-2); font-size: var(--text-xs); color: var(--warning); }
.save-bar { display: flex; align-items: center; gap: var(--sp-3); margin-top: var(--sp-4); padding-top: var(--sp-4); border-top: 1px solid var(--border); }
.msg { font-size: var(--text-sm); color: var(--success); }
@media (max-width: 768px) { .grid { grid-template-columns: 1fr; } }
</style>
