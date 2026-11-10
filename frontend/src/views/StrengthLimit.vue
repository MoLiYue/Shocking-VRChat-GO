<script setup lang="ts">
import { Lightbulb, Minus, Plus } from 'lucide-vue-next'
import { ref, onMounted } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { confirmDialog } from '@/dialog'

const { t } = useI18n()
// embedded: shown as a section of the combined setup page (Setup.vue)
const props = defineProps<{ embedded?: boolean }>()

const limitA = ref(5)
const limitB = ref(5)
const msg = ref('')
const msgType = ref<'ok' | 'err'>('ok')

let saveTimer: ReturnType<typeof setTimeout> | null = null

// Raising a limit by more than this in one go needs explicit confirmation.
const SAFE_STEP = 20
// Last values the backend accepted; a cancelled confirmation reverts to these.
const saved = { limitA: 5, limitB: 5 }

async function load() {
  const data = await api('/api/v1/strength_limit')
  limitA.value = data.channel_a ?? 5
  limitB.value = data.channel_b ?? 5
  Object.assign(saved, { limitA: limitA.value, limitB: limitB.value })
}

function current() {
  return { limitA: limitA.value, limitB: limitB.value }
}

let confirming = false
async function save() {
  if (confirming) return
  const cur = current()
  const jumps = (Object.keys(cur) as (keyof typeof cur)[])
    .filter((k) => cur[k] - saved[k] > SAFE_STEP)
  if (jumps.length) {
    confirming = true
    const names: Record<string, string> = {
      limitA: 'A ' + t('strength.strengthLabel'), limitB: 'B ' + t('strength.strengthLabel'),
    }
    const detail = jumps.map((k) => `${names[k]}: ${saved[k]} → ${cur[k]}`).join('\n')
    const ok = await confirmDialog({
      title: t('strength.bigJumpTitle'),
      message: t('strength.bigJumpBody', { step: SAFE_STEP }) + '\n\n' + detail,
      confirmText: t('strength.bigJumpConfirm'),
      danger: true,
    })
    confirming = false
    if (!ok) {
      limitA.value = saved.limitA; limitB.value = saved.limitB
      return
    }
  }
  const data = await apiPost('/api/v1/strength_limit', {
    channel_a: limitA.value,
    channel_b: limitB.value,
  })
  if (data.success) {
    Object.assign(saved, current())
    // same page: overlimit percent previews follow the new limits
    window.dispatchEvent(new Event('strength-limits-changed'))
    msg.value = t('common.saved')
    msgType.value = 'ok'
  } else {
    msg.value = t('common.saveFailed')
    msgType.value = 'err'
  }
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => msg.value = '', 2000)
}

function schedSave() {
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(save, 300)
}

function clamp(val: number) { return Math.max(0, Math.min(200, val)) }

function onInput() {
  limitA.value = clamp(limitA.value)
  limitB.value = clamp(limitB.value)
  schedSave()
}

function adj(target: 'limitA' | 'limitB', delta: number) {
  if (target === 'limitA') limitA.value = clamp(limitA.value + delta)
  else if (target === 'limitB') limitB.value = clamp(limitB.value + delta)
  schedSave()
}

onMounted(load)
</script>

<template>
  <div>
    <template v-if="!props.embedded">
      <h1 class="gradient-text page-title">{{ t('strength.title') }}</h1>
      <p class="page-desc">{{ t('strength.desc') }}</p>
    </template>

    <div class="recommend-banner">
      <Lightbulb class="recommend-icon" :size="18" />
      <span v-html="t('strength.recommend', { max: '<strong>200</strong>', default: '<strong>5</strong>' })"></span>
    </div>

    <div class="limit-grid">
      <section class="card ch-a">
        <h2 class="ch-head"><span class="ch-tag">A</span>{{ t('common.channelA') }}</h2>
        <div class="field">
          <label>{{ t('strength.strengthLabel') }}</label>
          <div class="slider-row" data-tour="strength-slider">
            <input type="range" v-model.number="limitA" min="0" max="200" step="1" @input="onInput">
          </div>
          <div class="value-row">
            <button class="adj-btn" aria-label="-1" @click="adj('limitA', -1)"><Minus :size="14" /></button>
            <input type="number" v-model.number="limitA" min="0" max="200" class="num-input" @input="onInput">
            <button class="adj-btn" aria-label="+1" @click="adj('limitA', 1)"><Plus :size="14" /></button>
          </div>
        </div>
      </section>

      <section class="card ch-b">
        <h2 class="ch-head"><span class="ch-tag">B</span>{{ t('common.channelB') }}</h2>
        <div class="field">
          <label>{{ t('strength.strengthLabel') }}</label>
          <div class="slider-row">
            <input type="range" v-model.number="limitB" min="0" max="200" step="1" @input="onInput">
          </div>
          <div class="value-row">
            <button class="adj-btn" aria-label="-1" @click="adj('limitB', -1)"><Minus :size="14" /></button>
            <input type="number" v-model.number="limitB" min="0" max="200" class="num-input" @input="onInput">
            <button class="adj-btn" aria-label="+1" @click="adj('limitB', 1)"><Plus :size="14" /></button>
          </div>
        </div>
      </section>
    </div>

    <div class="status-bar" v-if="msg">
      <span class="msg" :class="{ ok: msgType === 'ok', err: msgType === 'err' }">{{ msg }}</span>
    </div>

    <div class="info-card card">
      <h3>{{ t('strength.infoTitle') }}</h3>
      <ul>
        <li><code>{{ t('strength.info1') }}</code></li>
        <li>{{ t('strength.info2') }}</li>
        <li>{{ t('strength.info3') }}</li>
        <li>{{ t('strength.info4') }}</li>
      </ul>
      <h3 style="margin-top:var(--sp-4)">{{ t('strength.overlimitTitle') }}</h3>
      <ul>
        <li><router-link :to="props.embedded ? { path: '/params', hash: '#overlimit' } : '/overlimit-rules'">{{ t('strength.overlimit1') }}</router-link></li>
        <li>{{ t('strength.overlimit2') }}</li>
        <li>{{ t('strength.overlimit4') }}</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.recommend-banner { display: flex; align-items: flex-start; gap: var(--sp-3); padding: var(--sp-4); margin-bottom: var(--sp-5); background: rgba(var(--accent-rgb), 0.08); border: 1px solid rgba(var(--accent-rgb), 0.25); border-radius: var(--radius-lg); font-size: var(--text-sm); color: var(--text-secondary); line-height: 1.6; }
.recommend-icon { color: var(--warning); flex-shrink: 0; margin-top: 2px; }
.recommend-banner strong { color: var(--accent); }
.limit-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-4); margin-bottom: var(--sp-4); align-items: stretch; }
.limit-grid .card { display: flex; flex-direction: column; height: 100%; }
.field { margin-bottom: 0; }
.field label:not(.toggle-row) { display: block; font-size: var(--text-sm); color: var(--text-label); margin-bottom: var(--sp-2); font-weight: 500; }
.slider-row { display: flex; align-items: center; gap: var(--sp-2); }
.slider-row input[type="range"] { flex: 1; accent-color: var(--accent); }
.value-row { display: flex; align-items: center; justify-content: center; gap: var(--sp-2); margin-top: var(--sp-2); }
.adj-btn {
  width: 32px; height: 32px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: var(--bg-elevated);
  color: var(--text-secondary);
  font-size: var(--text-lg);
  font-weight: 600;
  cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: var(--transition-props);
  user-select: none;
}
.adj-btn:hover { border-color: var(--accent); color: var(--accent); background: rgba(var(--accent-rgb), 0.08); }
.adj-btn:active { transform: scale(0.92); }
.num-input { width: 64px; text-align: center; font-variant-numeric: tabular-nums; font-size: var(--text-base); font-weight: 600; }
.bar-wrap { margin-top: var(--sp-2); height: 6px; background: var(--bg-tertiary); border-radius: 3px; overflow: hidden; }
.ch-head { display: flex; align-items: center; gap: var(--sp-2); }
.bar-fill { height: 100%; background: var(--ch-color, var(--accent)); border-radius: 3px; transition: width 0.15s ease; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-2); }
.hint strong { color: var(--accent); }
.status-bar { margin-bottom: var(--sp-4); padding: var(--sp-2) var(--sp-4); background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-md); text-align: center; }
.msg { font-size: var(--text-sm); color: var(--text-muted); transition: color 0.2s; }
.msg.ok { color: var(--success); }
.msg.err { color: var(--danger); }
.info-card { font-size: var(--text-sm); color: var(--text-label); }
.info-card h3 { font-size: var(--text-base); margin-bottom: var(--sp-2); color: var(--text); }
.info-card ul { padding-left: var(--sp-4); }
.info-card li { margin-bottom: var(--sp-2); line-height: 1.6; }
.info-card code { background: var(--bg-tertiary); padding: 2px 6px; border-radius: var(--radius-sm); font-size: var(--text-xs); }
@media (max-width: 768px) { .limit-grid { grid-template-columns: 1fr; } }
</style>
