<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'

const { t } = useI18n()

const enabled = ref(false)
const targetHost = ref('127.0.0.1')
const targetPort = ref(9000)
const intervalSeconds = ref(3.0)
const sendNotification = ref(true)
const triggerSfx = ref(false)
const messageTemplate = ref('Shocking VRChat ⚡ A:{strength_a} B:{strength_b}')

const msg = ref('')
const msgErr = ref(false)
const testMsg = ref('')
const testMsgErr = ref(false)

const availableVars = [
  { key: '{strength_a}', desc: 'chatbox.varStrengthA' },
  { key: '{strength_b}', desc: 'chatbox.varStrengthB' },
  { key: '{mode_a}', desc: 'chatbox.varModeA' },
  { key: '{mode_b}', desc: 'chatbox.varModeB' },
  { key: '{device_count}', desc: 'chatbox.varDeviceCount' },
  { key: '{app_version}', desc: 'chatbox.varAppVersion' },
]

const previewMessage = computed(() => {
  let msg = messageTemplate.value
  const demoVars: Record<string, string> = {
    strength_a: '35',
    strength_b: '20',
    strength_limit_a: '100',
    strength_limit_b: '100',
    mode_a: 'distance',
    mode_b: 'shock',
    device_count: '1',
    app_version: '0.6.3',
  }
  try {
    for (const [k, v] of Object.entries(demoVars)) {
      msg = msg.split(`{${k}}`).join(v)
    }
  } catch {}
  return msg
})

async function load() {
  const data = await api('/api/v1/chatbox')
  enabled.value = data.enabled ?? false
  targetHost.value = data.target_host ?? '127.0.0.1'
  targetPort.value = data.target_port ?? 9000
  intervalSeconds.value = data.interval_seconds ?? 3.0
  sendNotification.value = data.send_notification ?? true
  triggerSfx.value = data.trigger_sfx ?? false
  messageTemplate.value = data.message_template ?? 'Shocking VRChat ⚡ A:{strength_a} B:{strength_b}'
}

async function save() {
  try {
    const data = await apiPost('/api/v1/chatbox', {
      enabled: enabled.value,
      target_host: targetHost.value,
      target_port: targetPort.value,
      interval_seconds: intervalSeconds.value,
      send_notification: sendNotification.value,
      trigger_sfx: triggerSfx.value,
      message_template: messageTemplate.value,
    })
    if (data.success) {
      msg.value = t('common.saved')
      msgErr.value = false
    } else {
      msg.value = t('common.saveFailed')
      msgErr.value = true
    }
  } catch {
    msg.value = t('common.saveFailed')
    msgErr.value = true
  }
  setTimeout(() => msg.value = '', 3000)
}

async function sendTest() {
  try {
    const data = await apiPost('/api/v1/chatbox/test', {
      message: previewMessage.value,
      target_host: targetHost.value,
      target_port: targetPort.value,
    })
    if (data.success) {
      testMsg.value = t('chatbox.testSent')
      testMsgErr.value = false
    } else {
      testMsg.value = data.error || t('chatbox.testFailed')
      testMsgErr.value = true
    }
  } catch {
    testMsg.value = t('chatbox.testFailed')
    testMsgErr.value = true
  }
  setTimeout(() => testMsg.value = '', 3000)
}

function insertVar(varKey: string) {
  messageTemplate.value += varKey
}

onMounted(load)
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('chatbox.title') }}</h1>
    <p class="page-desc">{{ t('chatbox.desc') }}</p>

    <!-- Enable toggle -->
    <section class="card">
      <h2>{{ t('chatbox.enableTitle') }}</h2>
      <label class="toggle-row">
        <input type="checkbox" v-model="enabled" />
        <span>{{ enabled ? t('common.enabled') : t('common.disabled') }}</span>
      </label>
    </section>

    <!-- Message Template -->
    <section class="card">
      <h2>{{ t('chatbox.templateTitle') }}</h2>
      <p class="page-desc" style="margin-bottom:var(--sp-4)">{{ t('chatbox.templateDesc') }}</p>
      <div class="field">
        <label>{{ t('chatbox.templateLabel') }}</label>
        <textarea v-model="messageTemplate" rows="3" class="template-input"></textarea>
      </div>
      <div class="field">
        <label>{{ t('chatbox.availableVars') }}</label>
        <div class="var-tags">
          <button
            v-for="v in availableVars"
            :key="v.key"
            class="var-tag"
            @click="insertVar(v.key)"
            :title="t(v.desc)"
          >
            {{ v.key }}
          </button>
        </div>
        <p class="hint">{{ t('chatbox.varClickHint') }}</p>
      </div>
      <div class="field">
        <label>{{ t('chatbox.preview') }}</label>
        <div class="preview-box">{{ previewMessage }}</div>
      </div>
    </section>

    <!-- OSC Settings -->
    <section class="card">
      <h2>{{ t('chatbox.oscTitle') }}</h2>
      <div class="osc-grid">
        <div class="field">
          <label>{{ t('chatbox.targetHost') }}</label>
          <input type="text" v-model="targetHost" />
          <p class="hint">{{ t('chatbox.targetHostHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('chatbox.targetPort') }}</label>
          <input type="number" v-model.number="targetPort" />
          <p class="hint">{{ t('chatbox.targetPortHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('chatbox.interval') }}</label>
          <input type="number" v-model.number="intervalSeconds" step="0.5" min="0.5" />
          <p class="hint">{{ t('chatbox.intervalHint') }}</p>
        </div>
      </div>
      <div class="field">
        <label class="toggle-row">
          <input type="checkbox" v-model="sendNotification" />
          <span>{{ t('chatbox.sendNotification') }}</span>
        </label>
        <p class="hint">{{ t('chatbox.sendNotificationHint') }}</p>
      </div>
      <div class="field">
        <label class="toggle-row">
          <input type="checkbox" v-model="triggerSfx" />
          <span>{{ t('chatbox.triggerSfx') }}</span>
        </label>
      </div>
    </section>

    <!-- Actions -->
    <div class="save-bar">
      <button class="btn btn-primary" @click="save">{{ t('chatbox.save') }}</button>
      <button class="btn btn-ghost" @click="sendTest">{{ t('chatbox.testSend') }}</button>
      <button class="btn btn-ghost" @click="load">{{ t('common.reload') }}</button>
      <span class="msg" :class="{ err: msgErr }">{{ msg }}</span>
      <span class="msg" :class="{ err: testMsgErr }">{{ testMsg }}</span>
    </div>

    <!-- Info -->
    <div class="info-card card">
      <h3>{{ t('chatbox.infoTitle') }}</h3>
      <ul>
        <li>{{ t('chatbox.info1') }}</li>
        <li>{{ t('chatbox.info2') }}</li>
        <li>{{ t('chatbox.info3') }}</li>
        <li>{{ t('chatbox.info4') }}</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.field { margin-bottom: var(--sp-4); }
.field:last-child { margin-bottom: 0; }
.field label { display: block; font-size: var(--text-sm); color: var(--text-secondary); margin-bottom: var(--sp-2); font-weight: 500; }
.field input { width: 100%; }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin-top: var(--sp-1); }

.toggle-row { display: flex; align-items: center; gap: var(--sp-2); cursor: pointer; font-size: var(--text-sm); margin-bottom: 0; }
.toggle-row input[type="checkbox"] { width: auto; margin: 0; cursor: pointer; }

.template-input {
  width: 100%;
  padding: var(--sp-3) var(--sp-4);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: #1a1225;
  color: var(--text);
  font-size: var(--text-sm);
  font-family: var(--font-mono);
  resize: vertical;
  outline: none;
  transition: all var(--transition);
}
.template-input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(139, 92, 246, 0.15), var(--glow-sm);
}

.var-tags { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.var-tag {
  padding: 2px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  cursor: pointer;
  transition: all var(--transition);
}
.var-tag:hover { border-color: var(--accent); color: var(--accent); background: rgba(139,92,246,0.06); }

.preview-box {
  background: rgba(139, 92, 246, 0.08);
  border: 1px solid rgba(139, 92, 246, 0.2);
  border-radius: var(--radius-md);
  padding: var(--sp-3) var(--sp-4);
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text);
  word-break: break-all;
  min-height: 24px;
}

.osc-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: var(--sp-4); margin-bottom: var(--sp-4); }

.save-bar { display: flex; align-items: center; gap: var(--sp-3); flex-wrap: wrap; margin-top: var(--sp-4); padding: var(--sp-4); background: var(--bg-card); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.msg { font-size: var(--text-sm); color: var(--success); }
.msg.err { color: var(--danger); }

.info-card { margin-top: var(--sp-4); font-size: var(--text-sm); color: var(--text-secondary); }
.info-card h3 { font-size: var(--text-base); margin-bottom: var(--sp-2); color: var(--text); }
.info-card ul { padding-left: var(--sp-4); }
.info-card li { margin-bottom: var(--sp-2); line-height: 1.6; }

@media (max-width: 768px) {
  .osc-grid { grid-template-columns: 1fr; }
}
</style>
