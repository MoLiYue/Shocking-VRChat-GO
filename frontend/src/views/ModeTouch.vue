<script setup lang="ts">
import AppSelect from '@/components/AppSelect.vue'
import { useUnsaved } from '@/unsaved'
import AutoSaveStatus from '@/components/AutoSaveStatus.vue'
import { RotateCcw } from 'lucide-vue-next'
import { ref, onMounted } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import WavePreview from '@/components/WavePreview.vue'
import AdvancedSection from '@/components/AdvancedSection.vue'

const { t } = useI18n()
const ch = ref<'a' | 'b'>('a')
const wavePreset = ref('')
const waveScale = ref(1.0)
const nDerivative = ref(1)
const textureFloor = ref(0)
const sampleStep = ref(1.0)
const advanceSamples = ref(4.0)
const envelopeCurve = ref('smoothstep')
const triggerBottom = ref(0)
const triggerTop = ref(0.8)
const presets = ref<string[]>([])
const msg = ref('')


async function loadPresets() {
  const data = await api('/api/v1/wave_presets')
  presets.value = data.presets || []
}

const unsaved = useUnsaved(() => ({ ch: ch.value, wavePreset: wavePreset.value, waveScale: waveScale.value, nDerivative: nDerivative.value, textureFloor: textureFloor.value, sampleStep: sampleStep.value, advanceSamples: advanceSamples.value, envelopeCurve: envelopeCurve.value, triggerBottom: triggerBottom.value, triggerTop: triggerTop.value }), { save: save })

async function load() {
  unsaved.markLoading()
  const data = await api(`/api/v1/mode_config/${ch.value}/touch`)
  const cfg = data.config || {}
  wavePreset.value = cfg.wave_preset || ''
  waveScale.value = cfg.wave_scale ?? 1.0
  nDerivative.value = cfg.n_derivative ?? 1
  textureFloor.value = cfg.texture_floor ?? 0
  sampleStep.value = cfg.wave_sample_step ?? 1.0
  advanceSamples.value = cfg.wave_advance_samples ?? 4.0
  envelopeCurve.value = cfg.wave_envelope_curve || 'smoothstep'
  triggerBottom.value = data.trigger_range?.bottom ?? 0
  triggerTop.value = data.trigger_range?.top ?? 0.8
  unsaved.markClean()
}

async function save() {
  const data = await apiPost(`/api/v1/mode_config/${ch.value}/touch`, {
    wave_preset: wavePreset.value || null,
    wave_scale: waveScale.value,
    n_derivative: nDerivative.value,
    texture_floor: textureFloor.value,
    wave_sample_step: sampleStep.value,
    wave_advance_samples: advanceSamples.value,
    wave_envelope_curve: envelopeCurve.value,
    trigger_range: { bottom: triggerBottom.value, top: triggerTop.value },
  })
  // Success is shown by the auto-save indicator; only report failures.
  msg.value = data.success ? '' : t('common.saveFailed')
  if (!data.success) setTimeout(() => msg.value = '', 3000)
  if (data.success) unsaved.markClean()
}

async function switchCh(c: 'a' | 'b') {
  if (c === ch.value || !(await unsaved.confirmDiscard())) return
  ch.value = c; load()
}
onMounted(() => { loadPresets(); load() })
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('modeTouch.title') }}</h1>
    <p class="page-desc">{{ t('modeTouch.desc') }}</p>

    <div class="ch-tabs">
      <button :class="{ active: ch === 'a' }" @click="switchCh('a')">{{ t('common.channelA') }}</button>
      <button :class="{ active: ch === 'b' }" @click="switchCh('b')">{{ t('common.channelB') }}</button>
    </div>

    <div class="grid">
      <section class="card">
        <h2>{{ t('modeTouch.params') }}</h2>
        <div class="field">
          <label>{{ t('modeTouch.derivative') }}</label>
          <AppSelect v-model="nDerivative" :options="[{ value: 0, label: t('modeTouch.deriv0') }, { value: 1, label: t('modeTouch.deriv1') }, { value: 2, label: t('modeTouch.deriv2') }, { value: 3, label: t('modeTouch.deriv3') }]" />
          <p class="hint">{{ t('modeTouch.derivHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('common.wavePreset') }}</label>
          <AppSelect v-model="wavePreset" :options="[{ value: '', label: t('modeTouch.presetNone') }, ...(presets).map((p) => ({ value: p, label: p }))]" />
        </div>
        <div class="field">
          <label>{{ t('common.waveScale') }}: {{ (waveScale * 100).toFixed(0) }}%</label>
          <input type="range" v-model.number="waveScale" min="0" max="1" step="0.05">
        </div>
        <div class="field">
          <label>{{ t('common.textureFloor') }}: {{ (textureFloor * 100).toFixed(0) }}%</label>
          <input type="range" v-model.number="textureFloor" min="0" max="0.5" step="0.01">
          <p class="hint">{{ t('modeTouch.floorHint') }}</p>
        </div>
        <div class="field" v-if="wavePreset">
          <label>{{ t('modeTouch.preview') }}</label>
          <WavePreview
            :preset-name="wavePreset"
            :wave-scale="waveScale"
            :texture-floor="textureFloor"
            :height="90"
          />
        </div>
      </section>

      <section class="card">
        <h2>{{ t('common.triggerRange') }}</h2>
        <div class="field">
          <label>{{ t('common.triggerBottom') }}: {{ triggerBottom.toFixed(2) }}</label>
          <input type="range" v-model.number="triggerBottom" min="0" max="0.9" step="0.01">
          <p class="hint">{{ t('modeTouch.bottomHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('common.triggerTop') }}: {{ triggerTop.toFixed(2) }}</label>
          <input type="range" v-model.number="triggerTop" min="0.1" max="1" step="0.01">
          <p class="hint">{{ t('modeTouch.topHint') }}</p>
        </div>
        <div class="visual">
          <div class="bar">
            <div class="fill" :style="{ left: (triggerBottom*100)+'%', width: ((triggerTop-triggerBottom)*100)+'%' }"></div>
            <span class="mark" :style="{ left: (triggerBottom*100)+'%' }">{{ triggerBottom.toFixed(2) }}</span>
            <span class="mark" :style="{ left: (triggerTop*100)+'%' }">{{ triggerTop.toFixed(2) }}</span>
          </div>
        </div>

        <AdvancedSection>
        <div class="field">
          <label>{{ t('modeDistance.sampleStep') }}: {{ sampleStep.toFixed(2) }}</label>
          <input type="range" v-model.number="sampleStep" min="0.25" max="4" step="0.25">
        </div>
        <div class="field">
          <label>{{ t('modeDistance.windowAdvance') }}: {{ advanceSamples.toFixed(1) }}</label>
          <input type="range" v-model.number="advanceSamples" min="1" max="16" step="0.5">
        </div>
        <div class="field">
          <label>{{ t('modeDistance.envelope') }}</label>
          <AppSelect v-model="envelopeCurve" :options="[{ value: 'smoothstep', label: 'smoothstep' }, { value: 'linear', label: 'linear' }, { value: 'ease_in', label: 'ease_in' }, { value: 'ease_out', label: 'ease_out' }]" />
        </div>
        </AdvancedSection>
      </section>
    </div>


    <div class="save-bar sticky">
      <AutoSaveStatus :state="unsaved.state.value" @retry="unsaved.flush()" />
      <button class="btn btn-ghost" @click="load"><RotateCcw :size="14" />{{ t('common.reload') }}</button>
      <span class="msg">{{ msg }}</span>
    </div>
  </div>
</template>

<style scoped>
.ch-tabs { display: flex; gap: var(--sp-2); margin-bottom: var(--sp-4); }
.ch-tabs button { padding: var(--sp-2) var(--sp-4); border: 1px solid var(--border); border-radius: var(--radius-md); background: transparent; color: var(--text-muted); cursor: pointer; font-size: var(--text-sm); }
.ch-tabs button.active { border-color: var(--accent); color: var(--accent); background: rgba(var(--accent-rgb), 0.08); }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-4); }
.field { margin-bottom: var(--sp-4); }
.field label:not(.toggle-row) { display: block; font-size: var(--text-sm); color: var(--text-label); margin-bottom: var(--sp-1); font-weight: 500; }
.field .app-select { width: 100%; }
.field input[type="range"] { width: 100%; accent-color: var(--accent); }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-1); }
.visual { margin-top: var(--sp-3); }
.bar { position: relative; height: 8px; background: var(--bg-inset); border-radius: 4px; }
.fill { position: absolute; top: 0; height: 100%; background: var(--accent); border-radius: 4px; opacity: 0.4; }
.mark { position: absolute; top: 14px; font-size: var(--text-2xs); color: var(--text-muted); transform: translateX(-50%); }
.msg { font-size: var(--text-sm); color: var(--success); }
@media (max-width: 768px) { .grid { grid-template-columns: 1fr; } }
</style>
