<script setup lang="ts">
import AppSelect from '@/components/AppSelect.vue'
import { useUnsaved } from '@/unsaved'
import { ArrowDown, ArrowUp, Plus, RefreshCw, RotateCcw, Trash2 } from 'lucide-vue-next'
import { ref, onMounted } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'

const { t } = useI18n()

interface CurvePoint { x: number; y: number }

// Must match DecayCurves / decayRemaining in internal/app/features.go.
const CURVES: Record<string, (t: number) => number> = {
  linear: (t) => 1 - t,
  ease_out: (t) => (1 - t) * (1 - t),
  ease_in: (t) => 1 - t * t,
  smoothstep: (t) => 1 - t * t * (3 - 2 * t),
  exp: (t) => (Math.exp(-4 * t) - Math.exp(-4)) / (1 - Math.exp(-4)),
  hold: () => 1,
}
const CURVE_NAMES = ['linear', 'ease_out', 'ease_in', 'smoothstep', 'exp', 'hold']

function customPoints(c: Rule['decay_curve']): CurvePoint[] {
  if (!Array.isArray(c)) return []
  const pts = c.map(p => ({ x: clamp01(p.x / 1), y: clamp01(p.y / 1) })).sort((a, b) => a.x - b.x)
  if (!pts.length) return []
  if (pts[0].x > 0) pts.unshift({ x: 0, y: 1 })
  if (pts[pts.length - 1].x < 1) pts.push({ x: 1, y: 0 })
  return pts
}
function clamp01(v: number) { return Number.isFinite(v) ? Math.min(1, Math.max(0, v)) : 0 }

function remaining(c: Rule['decay_curve'], t: number): number {
  if (t <= 0) return 1
  if (t >= 1) return 0
  const pts = customPoints(c)
  if (pts.length) {
    for (let i = 0; i + 1 < pts.length; i++) {
      const a = pts[i], b = pts[i + 1]
      if (t >= a.x && t <= b.x) return b.x - a.x <= 0 ? b.y : a.y + (t - a.x) / (b.x - a.x) * (b.y - a.y)
    }
    return pts[pts.length - 1].y
  }
  return clamp01((CURVES[c as string] || CURVES.linear)(t))
}

function curveKind(r: Rule): string { return Array.isArray(r.decay_curve) ? 'custom' : (r.decay_curve || 'linear') }
function setCurveKind(r: Rule, k: string) {
  if (k !== 'custom') { r.decay_curve = k; return }
  // Start the custom curve from the current preset shape.
  const prev = r.decay_curve
  r.decay_curve = [0.25, 0.5, 0.75].map(x => ({ x, y: Math.round(remaining(prev, x) * 100) / 100 }))
}
function addPoint(r: Rule) {
  const pts = Array.isArray(r.decay_curve) ? r.decay_curve : []
  pts.push({ x: 0.5, y: 0.5 })
  r.decay_curve = pts
}
function removePoint(r: Rule, i: number) {
  if (!Array.isArray(r.decay_curve)) return
  r.decay_curve.splice(i, 1)
  if (!r.decay_curve.length) r.decay_curve = 'linear'
}
// Percent views of a point for the inputs (stored as 0..1).
function pct(v: number) { return Math.round(v * 100) }
function setPct(p: CurvePoint, key: 'x' | 'y', ev: Event) {
  p[key] = clamp01(Number((ev.target as HTMLInputElement).value) / 100)
}

const PREVIEW_W = 160, PREVIEW_H = 48
function previewPath(r: Rule): string {
  const n = 48
  let d = ''
  for (let i = 0; i <= n; i++) {
    const t = i / n
    const x = (t * PREVIEW_W).toFixed(1)
    const y = ((1 - remaining(r.decay_curve, t >= 1 ? 0.9999 : t)) * PREVIEW_H).toFixed(1)
    d += (i ? ' L' : 'M') + x + ' ' + y
  }
  return d + ` L${PREVIEW_W} ${PREVIEW_H}`
}

interface Rule {
  name: string
  channel: 'a' | 'b' | 'both'
  limit_boost: number
  decay_seconds: number
  // Preset name or custom points (x = elapsed fraction, y = remaining fraction)
  decay_curve: string | CurvePoint[]
  condition: {
    param: string
    operator: string
    value: number
  }
  enabled: boolean
}

const rules = ref<Rule[]>([])
const effective = ref<{ A: number; B: number }>({ A: 0, B: 0 })
const msg = ref('')
const msgType = ref<'ok' | 'err'>('ok')

const operators = ['==', '!=', '>', '<', '>=', '<=']

const unsaved = useUnsaved(() => ({ rules: rules.value }))

async function load() {
  const data = await api('/api/v1/overlimit_rules')
  rules.value = data.rules || []
  effective.value = data.effective || { A: 0, B: 0 }
  unsaved.markClean()
}

function addRule() {
  rules.value.push({
    name: `${t('overlimit.rulePrefix')} ${rules.value.length + 1}`,
    channel: 'both',
    limit_boost: 20,
    decay_seconds: 10,
    decay_curve: 'linear',
    condition: {
      param: '/avatar/parameters/',
      operator: '==',
      value: 1,
    },
    enabled: true,
  })
}

async function save() {
  const data = await apiPost('/api/v1/overlimit_rules', { rules: rules.value })
  if (data.success) {
    msg.value = t('overlimit.savedSuccess')
    msgType.value = 'ok'
    rules.value = data.rules
  } else {
    msg.value = t('overlimit.saveFailed')
    msgType.value = 'err'
  }
  setTimeout(() => msg.value = '', 3000)
  if (data.success) unsaved.markClean()
}

async function removeRule(index: number) {
  rules.value.splice(index, 1)
  await save()
}

function moveUp(index: number) {
  if (index <= 0) return
  const tmp = rules.value[index]
  rules.value[index] = rules.value[index - 1]
  rules.value[index - 1] = tmp
}

function moveDown(index: number) {
  if (index >= rules.value.length - 1) return
  const tmp = rules.value[index]
  rules.value[index] = rules.value[index + 1]
  rules.value[index + 1] = tmp
}

onMounted(load)
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('overlimit.title') }}</h1>
    <p class="page-desc">{{ t('overlimit.desc') }}</p>

    <div class="status-bar">
      <span class="status-label">{{ t('overlimit.currentEffective') }}</span>
      <span class="status-chip" :class="{ active: effective.A > 0 }">A: {{ effective.A > 0 ? '+' + effective.A : '—' }}</span>
      <span class="status-chip" :class="{ active: effective.B > 0 }">B: {{ effective.B > 0 ? '+' + effective.B : '—' }}</span>
      <button class="icon-action" @click="load" :title="t('overlimit.refreshStatus')" :aria-label="t('overlimit.refreshStatus')"><RefreshCw :size="14" /></button>
    </div>

    <div class="rules-list">
      <div v-for="(rule, i) in rules" :key="i" class="rule-card card" :class="{ disabled: !rule.enabled }">
        <div class="rule-header">
          <input type="text" v-model="rule.name" class="rule-name" :placeholder="t('overlimit.ruleName')">
          <label class="toggle">
            <input type="checkbox" class="switch" role="switch" v-model="rule.enabled">
            <span class="toggle-label">{{ rule.enabled ? t('common.enable') : t('common.disable') }}</span>
          </label>
          <div class="rule-actions">
            <button class="icon-action" @click="moveUp(i)" :disabled="i === 0" :title="t('overlimit.moveUp')" :aria-label="t('overlimit.moveUp')"><ArrowUp :size="15" /></button>
            <button class="icon-action" @click="moveDown(i)" :disabled="i === rules.length - 1" :title="t('overlimit.moveDown')" :aria-label="t('overlimit.moveDown')"><ArrowDown :size="15" /></button>
            <button class="icon-action danger" @click="removeRule(i)" :title="t('common.delete')" :aria-label="t('common.delete')"><Trash2 :size="15" /></button>
          </div>
        </div>

        <div class="rule-body">
          <div class="condition-row">
            <span class="cond-label">{{ t('overlimit.when') }}</span>
            <input type="text" v-model="rule.condition.param" class="cond-param" placeholder="/avatar/parameters/...">
            <AppSelect v-model="rule.condition.operator" class="cond-op" :options="[...(operators).map((op) => ({ value: op, label: op }))]" />
            <input type="number" v-model.number="rule.condition.value" class="cond-value" step="0.01">
          </div>
          <div class="effect-row">
            <span class="cond-label">{{ t('overlimit.then') }}</span>
            <AppSelect v-model="rule.channel" class="effect-channel" :options="[{ value: 'a', label: t('overlimit.channelAOption') }, { value: 'b', label: t('overlimit.channelBOption') }, { value: 'both', label: t('overlimit.channelBothOption') }]" />
            <span class="effect-text">{{ t('overlimit.limitBoostBy') }}</span>
            <input type="number" v-model.number="rule.limit_boost" min="0" max="200" class="effect-value" :aria-label="t('overlimit.limitBoostBy')">
          </div>
          <div class="effect-row">
            <span class="cond-label"></span>
            <span class="effect-text">{{ t('overlimit.decayLabel') }}</span>
            <input type="number" v-model.number="rule.decay_seconds" min="0" max="600" step="0.5" class="effect-value" :aria-label="t('overlimit.decayLabel')">
            <span class="effect-text">{{ t('overlimit.decayUnit') }}</span>
          </div>
          <div v-if="rule.decay_seconds > 0" class="effect-row curve-row">
            <span class="cond-label"></span>
            <span class="effect-text">{{ t('overlimit.curveLabel') }}</span>
            <AppSelect :model-value="curveKind(rule)" @update:model-value="(k: string) => setCurveKind(rule, k)" class="curve-select"
              :options="[...CURVE_NAMES.map(n => ({ value: n, label: t('overlimit.curve_' + n) })), { value: 'custom', label: t('overlimit.curve_custom') }]" />
            <svg class="curve-preview" :viewBox="`0 0 ${PREVIEW_W} ${PREVIEW_H}`" :width="PREVIEW_W" :height="PREVIEW_H" role="img"
              :aria-label="t('overlimit.curvePreview')">
              <path :d="previewPath(rule)" class="curve-line" />
            </svg>
          </div>
          <div v-if="rule.decay_seconds > 0 && Array.isArray(rule.decay_curve)" class="points">
            <p class="points-hint">{{ t('overlimit.pointsHint') }}</p>
            <div v-for="(p, pi) in rule.decay_curve" :key="pi" class="point-row">
              <label>{{ t('overlimit.pointTime') }}
                <input type="number" min="0" max="100" step="1" :value="pct(p.x)" @change="setPct(p, 'x', $event)">%</label>
              <label>{{ t('overlimit.pointRemain') }}
                <input type="number" min="0" max="100" step="1" :value="pct(p.y)" @change="setPct(p, 'y', $event)">%</label>
              <button class="icon-action danger" @click="removePoint(rule, pi)" :title="t('common.delete')" :aria-label="t('common.delete')"><Trash2 :size="14" /></button>
            </div>
            <button class="btn btn-ghost btn-sm" @click="addPoint(rule)"><Plus :size="14" />{{ t('overlimit.addPoint') }}</button>
          </div>
        </div>
      </div>

      <div v-if="rules.length === 0" class="empty-state">
        <p>{{ t('overlimit.emptyState') }}</p>
      </div>
    </div>

    <div class="save-bar sticky">
      <button class="btn btn-ghost" @click="addRule"><Plus :size="15" />{{ t('overlimit.addRule') }}</button>
      <button class="btn btn-primary" @click="save">{{ t('overlimit.saveAll') }}</button>
      <button class="btn btn-ghost" @click="load"><RotateCcw :size="14" />{{ t('overlimit.reloadAll') }}</button>
      <span v-if="unsaved.dirty.value" class="unsaved-badge">{{ t('common.unsavedBadge') }}</span>
      <span class="msg" :class="{ ok: msgType === 'ok', err: msgType === 'err' }">{{ msg }}</span>
    </div>

    <div class="info-card card">
      <h3>{{ t('overlimit.infoTitle') }}</h3>
      <ul>
        <li><strong>{{ t('overlimit.infoCondition') }}</strong>：{{ t('overlimit.infoConditionDesc') }}</li>
        <li><strong>{{ t('overlimit.infoEffect') }}</strong>：{{ t('overlimit.infoEffectDesc') }}</li>
        <li><strong>{{ t('overlimit.infoMultiRule') }}</strong>：{{ t('overlimit.infoMultiRuleDesc') }}</li>
        <li><strong>{{ t('overlimit.infoPriority') }}</strong>：{{ t('overlimit.infoPriorityDesc') }}</li>
        <li><strong>{{ t('overlimit.infoRecover') }}</strong>：{{ t('overlimit.infoRecoverDesc') }}</li>
      </ul>
      <h3 style="margin-top:var(--sp-3)">{{ t('overlimit.infoExampleTitle') }}</h3>
      <ul>
        <li><code>/avatar/parameters/pcs/smash-intense == 1</code> → {{ t('overlimit.infoExample1') }}</li>
        <li><code>/avatar/parameters/pcs/contact/enterPass >= 0.9</code> → {{ t('overlimit.infoExample2') }}</li>
        <li><code>/avatar/parameters/Shock/OverrideMax == 1</code> → {{ t('overlimit.infoExample3') }}</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.status-bar { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-4); padding: var(--sp-3) var(--sp-4); background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.status-label { font-size: var(--text-sm); color: var(--text-muted); }
.status-chip { font-size: var(--text-sm); padding: var(--sp-1) var(--sp-3); border-radius: var(--radius-full); background: var(--bg-tertiary); color: var(--text-muted); font-variant-numeric: tabular-nums; }
.status-chip.active { background: rgba(var(--accent-rgb), 0.15); color: var(--accent); border: 1px solid rgba(var(--accent-rgb), 0.3); }
.rules-list { display: flex; flex-direction: column; gap: var(--sp-3); margin-bottom: var(--sp-4); }
.rule-card { padding: var(--sp-4); transition: opacity 0.2s; }
.rule-card.disabled { opacity: 0.5; }
.rule-header { display: flex; align-items: center; gap: var(--sp-3); margin-bottom: var(--sp-3); }
.rule-name { flex: 1; font-weight: 600; font-size: var(--text-sm); }
.toggle { display: flex; align-items: center; gap: var(--sp-2); font-size: var(--text-xs); color: var(--text-muted); cursor: pointer; white-space: nowrap; }
.toggle-label { user-select: none; }
.rule-actions { display: flex; gap: var(--sp-1); }
.rule-body { display: flex; flex-direction: column; gap: var(--sp-2); }
.condition-row, .effect-row { display: flex; align-items: center; gap: var(--sp-2); flex-wrap: wrap; }
.cond-label { font-size: var(--text-sm); color: var(--accent); font-weight: 600; min-width: 24px; }
.cond-param { flex: 1; min-width: 200px; font-size: var(--text-sm); }
.cond-op { width: 84px; min-width: 0; }
.cond-value { width: 80px; text-align: center; font-size: var(--text-sm); }
.effect-channel { width: 130px; min-width: 0; }
.effect-text { font-size: var(--text-sm); color: var(--text-secondary); }
.effect-value { width: 80px; text-align: center; font-size: var(--text-sm); font-weight: 600; }
.curve-select { width: 150px; min-width: 0; }
.curve-preview { background: var(--bg-tertiary); border-radius: var(--radius-sm); }
.curve-line { fill: rgba(var(--accent-rgb), 0.15); stroke: var(--accent); stroke-width: 1.5; }
.points { margin-left: 32px; display: flex; flex-direction: column; gap: var(--sp-1); }
.points-hint { font-size: var(--text-xs); color: var(--text-muted); margin: 0; }
.point-row { display: flex; align-items: center; gap: var(--sp-3); font-size: var(--text-sm); color: var(--text-secondary); }
.point-row input { width: 64px; text-align: center; margin: 0 var(--sp-1); }
.msg { font-size: var(--text-sm); color: var(--text-muted); }
.msg.ok { color: var(--success); }
.msg.err { color: var(--danger); }
.info-card { font-size: var(--text-sm); color: var(--text-label); }
.info-card h3 { font-size: var(--text-base); margin-bottom: var(--sp-2); color: var(--text); }
.info-card ul { padding-left: var(--sp-4); }
.info-card li { margin-bottom: var(--sp-2); line-height: 1.6; }
.info-card code { background: var(--bg-tertiary); padding: 2px 6px; border-radius: var(--radius-sm); font-size: var(--text-xs); }
.info-card em { color: var(--accent); font-style: normal; font-weight: 500; }
</style>
