<script setup lang="ts">
import { useUnsaved } from '@/unsaved'
import { themePref, setTheme, type ThemePref } from '@/theme'
import { Check, CircleCheck, CircleX, Compass, RotateCcw, ShieldCheck } from 'lucide-vue-next'
import { confirmDialog } from '@/dialog'
import { ref, onMounted, onUnmounted } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'
import { useTour } from '@/tour/useTour'

const { t } = useI18n()
const { restart: restartTour } = useTour()

function replayTour() {
  restartTour()
}

const oscPort = ref(9001)
const oscHost = ref('127.0.0.1')
const wsPort = ref(28846)
const v4Enabled = ref(true)
const webPort = ref(8800)
const webHost = ref('127.0.0.1')
const githubMirror = ref('')
const minimizeToTray = ref(false)
const trayAvailable = ref(false)
const logLevel = ref('INFO')
const msg = ref('')
const msgErr = ref(false)
const importMsg = ref('')
const importErr = ref(false)

const unsaved = useUnsaved(() => ({ oscPort: oscPort.value, oscHost: oscHost.value, wsPort: wsPort.value, v4Enabled: v4Enabled.value, webPort: webPort.value, webHost: webHost.value, githubMirror: githubMirror.value, logLevel: logLevel.value, minimizeToTray: minimizeToTray.value }))

async function load() {
  const data = await api('/api/v1/config')
  const adv = data.advanced || {}
  oscPort.value = adv.osc?.listen_port || 9001
  oscHost.value = adv.osc?.listen_host || '127.0.0.1'
  wsPort.value = adv.ws?.listen_port || 28846
  v4Enabled.value = adv.ws?.v4_enabled !== false
  webPort.value = adv.web_server?.listen_port || 8800
  webHost.value = adv.web_server?.listen_host || '127.0.0.1'
  logLevel.value = adv.log_level || 'INFO'
  githubMirror.value = adv.general?.github_mirror || ''
  minimizeToTray.value = !!adv.general?.minimize_to_tray
  try { trayAvailable.value = !!(await api('/api/v1/status')).tray_available } catch {}
  unsaved.markClean()
}

async function save() {
  const data = await apiPost('/api/v1/settings', {
    osc: { listen_port: oscPort.value, listen_host: oscHost.value },
    ws: { listen_port: wsPort.value, v4_enabled: v4Enabled.value },
    web_server: { listen_port: webPort.value, listen_host: webHost.value },
    log_level: logLevel.value,
    github_mirror: githubMirror.value,
    minimize_to_tray: minimizeToTray.value,
  })
  if (data.success) {
    if (data.restart_needed?.length) {
      msg.value = t('settings.savedRestarting')
      msgErr.value = false
      // If web port changed, redirect to new port after delay
      const newPort = webPort.value
      const currentPort = window.location.port
      if (String(newPort) !== currentPort) {
        setTimeout(() => {
          window.location.href = `http://${window.location.hostname}:${newPort}/settings`
        }, 3000)
      } else {
        setTimeout(() => window.location.reload(), 3000)
      }
    } else {
      msg.value = data.message; msgErr.value = false
    }
  } else { msg.value = data.message || t('common.saveFailed'); msgErr.value = true }
  setTimeout(() => msg.value = '', 8000)
  if (data.success) unsaved.markClean()
}

function exportConfig() {
  window.open('/api/v1/config/export', '_blank')
}

const importFileRef = ref<HTMLInputElement | null>(null)

function triggerImport() {
  importFileRef.value?.click()
}

async function handleImport(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  const form = new FormData()
  form.append('file', file)
  try {
    const res = await fetch('/api/v1/config/import', { method: 'POST', body: form })
    const data = await res.json()
    if (data.success) {
      importMsg.value = '✓ ' + data.message
      importErr.value = false
      setTimeout(() => window.location.reload(), 2000)
    } else {
      importMsg.value = '✗ ' + (data.detail || t('settings.importFailed'))
      importErr.value = true
    }
  } catch (err) {
    importMsg.value = t('settings.importFailed') + ': ' + err
    importErr.value = true
  }
  if (importFileRef.value) importFileRef.value.value = ''
  setTimeout(() => importMsg.value = '', 8000)
}

onMounted(load)

// --- Update check ---
const updateInfo = ref<any>(null)
const updateChecking = ref(false)
const updateApplying = ref(false)
const updateMsg = ref('')
const updateErr = ref(false)

// --- Update progress modal ---
const showUpdateModal = ref(false)
const updateStage = ref<string>('idle')   // downloading|verifying|extracting|applying|restarting|done|error
const updatePercent = ref(0)
const updateDownloaded = ref(0)
const updateTotal = ref(0)
const updateStageMsg = ref('')
const updateStageErr = ref('')
let progressTimer: number | null = null

const STAGES = ['downloading', 'verifying', 'extracting', 'applying', 'restarting']

const stageLabels: Record<string, string> = {
  downloading: 'updateStageDownloading',
  verifying: 'updateStageVerifying',
  extracting: 'updateStageExtracting',
  applying: 'updateStageApplying',
  restarting: 'updateStageRestarting',
}

function stageIndex(stage: string): number {
  const i = STAGES.indexOf(stage)
  return i < 0 ? (stage === 'done' ? STAGES.length : -1) : i
}

function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`
}

/** Turn raw network/GitHub errors into something a user can act on. */
function friendlyUpdateError(raw: string): string {
  const s = String(raw || '')
  if (/403|rate limit/i.test(s)) return t('settings.updateErrRateLimit')
  if (/404/.test(s)) return t('settings.updateErrNotFound')
  if (/timeout|timed out|deadline/i.test(s)) return t('settings.updateErrTimeout')
  if (/no such host|dial|connect|network|refused|Failed to fetch/i.test(s)) return t('settings.updateErrNetwork')
  return s || t('settings.updateFailed')
}

async function checkUpdate() {
  updateChecking.value = true
  updateMsg.value = ''
  try {
    const data = await api('/api/v1/update/check')
    updateInfo.value = data
    if (data.error) {
      updateMsg.value = friendlyUpdateError(data.error)
      updateErr.value = true
    }
  } catch (e) {
    updateMsg.value = t('settings.updateFailed')
    updateErr.value = true
  }
  updateChecking.value = false
}

function startProgressPolling() {
  stopProgressPolling()
  progressTimer = window.setInterval(async () => {
    try {
      const p = await api('/api/v1/update/progress')
      updateStage.value = p.stage
      updatePercent.value = p.percent || 0
      updateDownloaded.value = p.downloaded || 0
      updateTotal.value = p.total || 0
      updateStageMsg.value = p.message || ''
      if (p.stage === 'error') {
        updateStageErr.value = friendlyUpdateError(p.error)
        stopProgressPolling()
        updateApplying.value = false
      }
    } catch {
      // server may be restarting — that's expected in the restarting stage
    }
  }, 500)
}

function stopProgressPolling() {
  if (progressTimer !== null) {
    clearInterval(progressTimer)
    progressTimer = null
  }
}

async function applyUpdate() {
  if (!(await confirmDialog(t('settings.updateConfirm')))) return
  // Open modal + reset progress
  showUpdateModal.value = true
  updateApplying.value = true
  updateStage.value = 'downloading'
  updatePercent.value = 0
  updateDownloaded.value = 0
  updateTotal.value = updateInfo.value?.download_size || 0
  updateStageMsg.value = t('settings.updateProgressDownloading')
  updateStageErr.value = ''
  updateMsg.value = ''
  updateErr.value = false
  startProgressPolling()

  try {
    const res = await fetch('/api/v1/update/apply', { method: 'POST' })
    // If the server restarts before responding, fetch may reject — handled by poll.
    let data: any = null
    try { data = await res.json() } catch { /* server exited */ }
    if (data && data.success) {
      // Backend launched updater and will exit; move to restarting phase
      updateStage.value = 'restarting'
      updateStageMsg.value = t('settings.updateProgressRestarting')
      stopProgressPolling()
      pollForRestart()
    } else if (data && !data.success) {
      updateStage.value = 'error'
      updateStageErr.value = friendlyUpdateError(data.detail || data.message)
      stopProgressPolling()
      updateApplying.value = false
    } else {
      // No response body: server likely already exited — treat as restarting
      updateStage.value = 'restarting'
      updateStageMsg.value = t('settings.updateProgressRestarting')
      stopProgressPolling()
      pollForRestart()
    }
  } catch (e) {
    // Network error can mean the server already exited to apply the update.
    // If we already got past applying, treat as restarting; otherwise error.
    if (stageIndex(updateStage.value) >= stageIndex('applying')) {
      updateStage.value = 'restarting'
      updateStageMsg.value = t('settings.updateProgressRestarting')
      stopProgressPolling()
      pollForRestart()
    } else {
      updateStage.value = 'error'
      updateStageErr.value = friendlyUpdateError(String(e))
      stopProgressPolling()
      updateApplying.value = false
    }
  }
}

const updatePollTimedOut = ref(false)

function pollForRestart() {
  let attempts = 0
  const maxAttempts = 30 // 30 × 2s = 60s max
  updatePollTimedOut.value = false
  updateStage.value = 'restarting'
  updateStageMsg.value = t('settings.updateProgressWaiting')
  const interval = setInterval(async () => {
    attempts++
    try {
      const res = await fetch('/api/v1/status', { signal: AbortSignal.timeout(3000) })
      if (res.ok) {
        clearInterval(interval)
        updateStage.value = 'done'
        updatePercent.value = 100
        window.location.reload()
      }
    } catch {
      // Server not ready yet
    }
    if (attempts >= maxAttempts) {
      clearInterval(interval)
      updateMsg.value = t('settings.updatePollTimeout')
      updateErr.value = false
      updateApplying.value = false
      updatePollTimedOut.value = true
    }
  }, 2000)
}

function retryPoll() {
  updatePollTimedOut.value = false
  updateApplying.value = true
  updateErr.value = false
  pollForRestart()
}

function manualRefresh() {
  window.location.reload()
}

function closeUpdateModal() {
  // Only allow closing on error (otherwise update is in progress)
  if (updateStage.value === 'error' || updatePollTimedOut.value) {
    showUpdateModal.value = false
    stopProgressPolling()
  }
}

onMounted(() => { checkUpdate() })
onUnmounted(() => { stopProgressPolling() })
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('settings.title') }}</h1>
    <p class="page-desc">{{ t('settings.desc') }}</p>

    <div class="settings-grid">
      <section class="card">
        <h2>{{ t('settings.oscTitle') }}</h2>
        <div class="field">
          <label>{{ t('settings.oscPort') }}</label>
          <input type="number" v-model.number="oscPort" min="1024" max="65535">
          <p class="hint">{{ t('settings.oscPortHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('settings.oscHost') }}</label>
          <input type="text" v-model="oscHost">
          <p class="hint">{{ t('settings.oscHostHint') }}</p>
        </div>
      </section>

      <section class="card">
        <h2>{{ t('settings.wsTitle') }}</h2>
        <div class="field">
          <label>{{ t('settings.wsPort') }}</label>
          <input type="number" v-model.number="wsPort" min="1024" max="65535">
          <p class="hint">{{ t('settings.wsPortHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('settings.v4Title') }}</label>
          <label class="toggle-row">
            <input type="checkbox" class="switch" role="switch" v-model="v4Enabled">
            <span>{{ v4Enabled ? t('common.enabled') : t('common.disabled') }}</span>
          </label>
          <p class="hint">{{ t('settings.v4Hint') }}</p>
        </div>
      </section>

      <section class="card">
        <h2>{{ t('settings.webTitle') }}</h2>
        <div class="field">
          <label>{{ t('settings.webPort') }}</label>
          <input type="number" v-model.number="webPort" min="1024" max="65535">
          <p class="hint">{{ t('settings.webPortHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('settings.webHost') }}</label>
          <input type="text" v-model="webHost">
          <p class="hint">{{ t('settings.webHostHint') }}</p>
        </div>
      </section>

      <section class="card">
        <h2>{{ t('settings.logTitle') }}</h2>
        <div class="field">
          <label>{{ t('settings.logLevel') }}</label>
          <select v-model="logLevel">
            <option value="DEBUG">DEBUG</option>
            <option value="INFO">INFO</option>
            <option value="WARNING">WARNING</option>
            <option value="ERROR">ERROR</option>
          </select>
          <p class="hint">{{ t('settings.logLevelHint') }}</p>
        </div>
      </section>

      <section class="card">
        <h2>{{ t('settings.githubTitle') }}</h2>
        <div class="field">
          <label>{{ t('settings.githubMirror') }}</label>
          <input type="text" v-model="githubMirror" :placeholder="t('settings.githubMirrorPlaceholder')">
          <p class="hint">{{ t('settings.githubMirrorHint') }}</p>
          <div class="mirror-presets">
            <button class="preset-tag" @click="githubMirror = ''">{{ t('settings.githubDirect') }}</button>
            <button class="preset-tag" @click="githubMirror = 'https://mirror.ghproxy.com'">ghproxy</button>
            <button class="preset-tag" @click="githubMirror = 'https://ghfast.top'">ghfast</button>
            <button class="preset-tag" @click="githubMirror = 'https://gh-proxy.com'">gh-proxy</button>
          </div>
        </div>
      </section>

      <section class="card">
        <h2>{{ t('settings.appearanceTitle') }}</h2>
        <div class="field">
          <label>{{ t('settings.theme') }}</label>
          <div class="seg" role="radiogroup" :aria-label="t('settings.theme')">
            <button
              v-for="opt in (['system', 'dark', 'light'] as ThemePref[])" :key="opt"
              role="radio" :aria-checked="themePref === opt"
              :class="{ active: themePref === opt }" @click="setTheme(opt)"
            >{{ t('settings.theme' + opt[0].toUpperCase() + opt.slice(1)) }}</button>
          </div>
          <p class="hint">{{ t('settings.themeHint') }}</p>
        </div>
      </section>

      <section v-if="trayAvailable" class="card">
        <h2>{{ t('settings.windowTitle') }}</h2>
        <div class="field">
          <label class="toggle-row">
            <input type="checkbox" class="switch" role="switch" v-model="minimizeToTray">
            <span>{{ t('settings.minimizeToTray') }}</span>
          </label>
          <p class="hint">{{ t('settings.minimizeToTrayHint') }}</p>
        </div>
      </section>
    </div>

    <div class="save-bar sticky">
      <button class="btn btn-primary" @click="save">{{ t('settings.saveSettings') }}</button>
      <button class="btn btn-ghost" @click="load"><RotateCcw :size="14" />{{ t('settings.reloadSettings') }}</button>
      <span v-if="unsaved.dirty.value" class="unsaved-badge">{{ t('common.unsavedBadge') }}</span>
      <span class="msg" :class="{ err: msgErr }">{{ msg }}</span>
    </div>

    <section class="card" style="margin-top:var(--sp-4)">
      <h2>{{ t('settings.configTitle') }}</h2>
      <p class="hint" style="margin-bottom:var(--sp-3)">{{ t('settings.configDesc') }}</p>
      <div class="ie-bar">
        <button class="btn btn-ghost" @click="exportConfig">{{ t('settings.exportConfig') }}</button>
        <button class="btn btn-ghost" @click="triggerImport">{{ t('settings.importConfig') }}</button>
        <input ref="importFileRef" type="file" accept=".json" hidden @change="handleImport">
        <span class="msg" :class="{ err: importErr }">{{ importMsg }}</span>
      </div>
    </section>

    <section class="card" style="margin-top:var(--sp-4)">
      <h2>{{ t('settings.helpTitle') }}</h2>
      <p class="hint" style="margin-bottom:var(--sp-3)">{{ t('settings.helpDesc') }}</p>
      <div class="ie-bar">
        <button class="btn btn-ghost" @click="replayTour"><Compass :size="14" />{{ t('settings.tourRestart') }}</button>
        <a class="btn btn-ghost" href="https://github.com/MoLiYue/Shocking-VRChat-GO/blob/main/doc/dglab/安全须知.md" target="_blank" rel="noopener"><ShieldCheck :size="14" />{{ t('settings.safetyDoc') }}</a>
        <span class="hint shortcut-hint">{{ t('settings.shortcutsHint') }}</span>
      </div>
    </section>

    <section class="card" style="margin-top:var(--sp-4)">
      <h2>{{ t('settings.updateTitle') }}</h2>
      <div class="update-info">
        <div class="update-row">
          <span class="update-label">{{ t('settings.updateCurrent') }}:</span>
          <span class="update-value">{{ updateInfo?.current || '...' }}</span>
        </div>
        <div class="update-row">
          <span class="update-label">{{ t('settings.updateLatest') }}:</span>
          <span class="update-value" :class="{ 'has-update': updateInfo?.update_available }">
            {{ updateInfo?.latest || (updateChecking ? t('settings.checking') : t('settings.unknown')) }}
            <span v-if="updateInfo?.update_available" class="update-badge">{{ t('settings.updateAvailable') }}</span>
          </span>
        </div>
        <div v-if="updateInfo?.release_name && updateInfo?.update_available" class="update-row">
          <span class="update-label">{{ t('settings.updateNotes') }}:</span>
          <span class="update-value update-notes">{{ updateInfo.release_name }}</span>
        </div>
      </div>
      <div class="ie-bar" style="margin-top:var(--sp-3)">
        <button class="btn btn-ghost" @click="checkUpdate" :disabled="updateChecking || updateApplying">{{ t('settings.updateCheck') }}</button>
        <button
          v-if="updateInfo?.update_available && !updatePollTimedOut"
          class="btn btn-primary"
          @click="applyUpdate"
          :disabled="updateApplying"
        >{{ updateApplying ? t('settings.updateApplying') : t('settings.updateApply') }}</button>
        <button
          v-if="updatePollTimedOut"
          class="btn btn-primary"
          @click="retryPoll"
        >{{ t('settings.updateRetryPoll') }}</button>
        <button
          v-if="updatePollTimedOut"
          class="btn btn-ghost"
          @click="manualRefresh"
        >{{ t('settings.updateManualRefresh') }}</button>
        <span class="msg" :class="{ err: updateErr, waiting: updateApplying && !updateErr }">{{ updateMsg }}</span>
      </div>
    </section>

    <!-- Update progress modal -->
    <Teleport to="body">
      <div v-if="showUpdateModal" class="update-overlay">
        <div class="update-modal">
          <div class="update-modal-head">
            <h3>
              <span class="spinner" v-if="updateStage !== 'error' && updateStage !== 'done'"></span>
              <CircleCheck v-else-if="updateStage === 'done'" class="done-icon" :size="18" />
              <CircleX v-else class="err-icon" :size="18" />
              {{ t('settings.updateProgressTitle') }}
            </h3>
            <span class="ver-pill" v-if="updateInfo?.latest">v{{ updateInfo.latest }}</span>
          </div>

          <!-- Stepper -->
          <div class="stepper">
            <template v-for="(s, i) in STAGES" :key="s">
              <div
                class="step"
                :class="{
                  active: stageIndex(updateStage) === i,
                  done: stageIndex(updateStage) > i || updateStage === 'done',
                  failed: updateStage === 'error' && stageIndex(updateStage) === i,
                }"
              >
                <div class="step-dot">
                  <Check v-if="stageIndex(updateStage) > i || updateStage === 'done'" :size="13" :stroke-width="3" />
                  <span v-else>{{ i + 1 }}</span>
                </div>
                <div class="step-label">{{ t('settings.' + stageLabels[s]) }}</div>
              </div>
              <div v-if="i < STAGES.length - 1" class="step-line" :class="{ done: stageIndex(updateStage) > i || updateStage === 'done' }"></div>
            </template>
          </div>

          <!-- Progress bar -->
          <div class="progress-wrap" v-if="updateStage !== 'error'">
            <div class="progress-track">
              <div
                class="progress-fill"
                :class="{ indeterminate: updateStage !== 'downloading' && updateStage !== 'done' }"
                :style="updateStage === 'downloading' || updateStage === 'done' ? { width: updatePercent + '%' } : {}"
              ></div>
            </div>
            <div class="progress-meta">
              <span class="progress-stage">{{ updateStageMsg }}</span>
              <span class="progress-num" v-if="updateStage === 'downloading' && updateTotal > 0">
                {{ fmtBytes(updateDownloaded) }} / {{ fmtBytes(updateTotal) }} · {{ updatePercent }}%
              </span>
              <span class="progress-num" v-else-if="updateStage === 'downloading'">
                {{ fmtBytes(updateDownloaded) }}
              </span>
            </div>
          </div>

          <!-- Error box -->
          <div v-if="updateStage === 'error'" class="update-error-box">
            {{ updateStageErr }}
          </div>

          <p class="update-note" v-if="updateStage !== 'error' && !updatePollTimedOut">
            {{ t('settings.updateProgressNote') }}
          </p>

          <!-- Timeout actions -->
          <div v-if="updatePollTimedOut" class="update-error-box warn">
            {{ t('settings.updatePollTimeout') }}
          </div>

          <div class="update-modal-actions">
            <button v-if="updatePollTimedOut" class="btn btn-primary" @click="retryPoll">{{ t('settings.updateRetryPoll') }}</button>
            <button v-if="updatePollTimedOut" class="btn btn-ghost" @click="manualRefresh">{{ t('settings.updateManualRefresh') }}</button>
            <button
              v-if="updateStage === 'error' || updatePollTimedOut"
              class="btn btn-ghost"
              @click="closeUpdateModal"
            >{{ t('settings.updateClose') }}</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.page-desc { color: var(--text-muted); font-size: var(--text-sm); margin-bottom: var(--sp-6); }
.settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-4); }
.field { margin-bottom: var(--sp-4); }
.field:last-child { margin-bottom: 0; }
.field label:not(.toggle-row) { display: block; font-size: var(--text-sm); color: var(--text-label); margin-bottom: var(--sp-2); font-weight: 500; }
.toggle-row { display: flex; align-items: center; gap: var(--sp-2); cursor: pointer; font-size: var(--text-sm); }
.field input:not(.switch), .field select { width: 100%; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-1); }
.ie-bar { display: flex; align-items: center; gap: var(--sp-3); flex-wrap: wrap; }
.shortcut-hint { margin: 0 0 0 auto; }
.msg { font-size: var(--text-sm); color: var(--success); }
.msg.err { color: var(--danger); }
.msg.waiting { color: var(--accent); animation: pulse-text 1.5s ease-in-out infinite; }
@keyframes pulse-text { 0%,100% { opacity: 1; } 50% { opacity: 0.5; } }
.update-info { display: flex; flex-direction: column; gap: var(--sp-2); }
.update-row { display: flex; align-items: center; gap: var(--sp-2); font-size: var(--text-sm); }
.update-label { color: var(--text-muted); min-width: 80px; }
.update-value { color: var(--text-secondary); }
.update-value.has-update { color: var(--accent); font-weight: 600; }
.update-badge { display: inline-block; margin-left: var(--sp-2); padding: 1px 8px; border-radius: 99px; background: rgba(var(--accent-rgb), 0.15); color: var(--accent); font-size: var(--text-xs); font-weight: 600; }
.update-notes { font-size: var(--text-xs); color: var(--text-muted); max-width: 400px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mirror-presets { display: flex; gap: var(--sp-2); margin-top: var(--sp-2); flex-wrap: wrap; }
.preset-tag { padding: 2px 10px; border: 1px solid var(--border); border-radius: 99px; background: transparent; color: var(--text-muted); font-size: var(--text-xs); cursor: pointer; transition: var(--transition-props); }
.preset-tag:hover { border-color: var(--accent); color: var(--accent); background: rgba(var(--accent-rgb), 0.06); }

/* --- Update progress modal --- */
.update-overlay {
  position: fixed; inset: 0; z-index: 1000;
  background: var(--overlay);
  display: flex; align-items: center; justify-content: center;
  padding: var(--sp-4);
  animation: fade-in 0.2s ease;
}
@keyframes fade-in { from { opacity: 0; } to { opacity: 1; } }
.update-modal {
  width: 100%; max-width: 520px;
  background: var(--bg-card); border: 1px solid var(--border);
  border-radius: var(--radius-lg); padding: var(--sp-5);
  box-shadow: var(--shadow-pop);
  animation: modal-pop 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}
@keyframes modal-pop { from { transform: translateY(12px) scale(0.97); opacity: 0; } to { transform: none; opacity: 1; } }
.update-modal-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--sp-5); }
.update-modal-head h3 { display: flex; align-items: center; gap: var(--sp-2); margin: 0; font-size: var(--text-lg); }
.ver-pill { padding: 2px 10px; border-radius: 99px; background: rgba(var(--accent-rgb), 0.15); color: var(--accent); font-size: var(--text-xs); font-weight: 600; }
.done-icon { color: var(--success); }
.err-icon { color: var(--danger); }
.spinner {
  width: 16px; height: 16px; border-radius: 50%;
  border: 2px solid rgba(var(--accent-rgb), 0.25); border-top-color: var(--accent);
  animation: spin 0.8s linear infinite; display: inline-block;
}
@keyframes spin { to { transform: rotate(360deg); } }

.stepper { display: flex; align-items: center; margin-bottom: var(--sp-5); }
.step { display: flex; flex-direction: column; align-items: center; gap: var(--sp-1); flex-shrink: 0; }
.step-dot {
  width: 28px; height: 28px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  font-size: var(--text-xs); font-weight: 600;
  border: 2px solid var(--border); background: var(--bg); color: var(--text-muted);
  transition: var(--transition-props);
}
.step.active .step-dot { border-color: var(--accent); color: var(--accent); box-shadow: 0 0 0 4px rgba(var(--accent-rgb), 0.15); }
.step.done .step-dot { border-color: var(--success); background: var(--success); color: var(--text-on-accent); }
.step.failed .step-dot { border-color: var(--danger); color: var(--danger); }
.step-label { font-size: var(--text-2xs); color: var(--text-muted); white-space: nowrap; }
.step.active .step-label { color: var(--accent); font-weight: 600; }
.step.done .step-label { color: var(--text-secondary); }
.step-line { flex: 1; height: 2px; background: var(--border); margin: 0 4px; margin-bottom: 16px; transition: background var(--transition); }
.step-line.done { background: var(--success); }

.progress-wrap { margin-bottom: var(--sp-4); }
.progress-track { width: 100%; height: 8px; background: var(--bg); border-radius: 99px; overflow: hidden; }
.progress-fill {
  height: 100%; border-radius: 99px;
  background: linear-gradient(90deg, var(--accent), var(--accent-hover));
  transition: width 0.3s ease;
}
.progress-fill.indeterminate {
  width: 40% !important;
  animation: indeterminate 1.2s ease-in-out infinite;
}
@keyframes indeterminate {
  0% { margin-left: -40%; }
  100% { margin-left: 100%; }
}
.progress-meta { display: flex; justify-content: space-between; align-items: center; margin-top: var(--sp-2); gap: var(--sp-2); }
.progress-stage { font-size: var(--text-sm); color: var(--text-secondary); }
.progress-num { font-size: var(--text-xs); color: var(--text-muted); font-variant-numeric: tabular-nums; white-space: nowrap; }

.update-error-box { padding: var(--sp-3); border-radius: var(--radius); background: rgba(var(--red-rgb), 0.1); border: 1px solid rgba(var(--red-rgb), 0.3); color: var(--danger); font-size: var(--text-sm); margin-bottom: var(--sp-3); word-break: break-word; }
.update-error-box.warn { background: rgba(var(--yellow-rgb), 0.1); border-color: var(--warning-border); color: var(--warning); }
.update-note { font-size: var(--text-xs); color: var(--text-muted); margin: 0 0 var(--sp-3); }
.update-modal-actions { display: flex; gap: var(--sp-2); justify-content: flex-end; }
.update-modal-actions:empty { display: none; }

@media (max-width: 768px) { .settings-grid { grid-template-columns: 1fr; } }
</style>
