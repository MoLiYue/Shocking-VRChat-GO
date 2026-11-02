<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { api, apiPost } from '@/api'
import { useI18n } from '@/i18n'

const { t } = useI18n()

const enabled = ref(true)
const heartbeat = ref(true)
const interval = ref(5.0)
const targetHost = ref('127.0.0.1')
const targetPort = ref(9000)

type Zone = {
  trigger_type: string
  zone_id: string
  kind: string
  suggested_path: string
  last_seen: number
}
const zones = ref<Zone[]>([])
const ogbParams = ref<Record<string, { path: string; mode: string; enabled: boolean }[]>>({ a: [], b: [] })

const msg = ref('')
const msgErr = ref(false)
const addMsg = ref('')
const addMsgErr = ref(false)
let zoneTimer: number | undefined

function flash(target: 'save' | 'add', text: string, isErr: boolean) {
  if (target === 'save') { msg.value = text; msgErr.value = isErr }
  else { addMsg.value = text; addMsgErr.value = isErr }
  setTimeout(() => { if (target === 'save') msg.value = ''; else addMsg.value = '' }, 3000)
}

async function load() {
  try {
    const data = await api('/api/v1/sps')
    const cfg = data.sps ?? {}
    enabled.value = cfg.enabled ?? true
    heartbeat.value = cfg.ogb_enabled_heartbeat ?? true
    interval.value = cfg.ogb_enabled_interval ?? 5.0
    targetHost.value = cfg.ogb_target_host ?? '127.0.0.1'
    targetPort.value = cfg.ogb_target_port ?? 9000
    ogbParams.value = data.ogb_params ?? { a: [], b: [] }
  } catch {
    flash('save', t('common.loadFailed'), true)
  }
}

async function loadZones() {
  try {
    const data = await api('/api/v1/sps/zones')
    zones.value = data.zones ?? []
  } catch {
    // 轮询失败不打扰用户，保留上次结果
  }
}

async function save() {
  try {
    const data = await apiPost('/api/v1/sps', {
      enabled: enabled.value,
      ogb_enabled_heartbeat: heartbeat.value,
      ogb_enabled_interval: interval.value,
      ogb_target_host: targetHost.value,
      ogb_target_port: targetPort.value,
    })
    flash('save', data.success ? t('common.saved') : t('common.saveFailed'), !data.success)
    if (data.success) await load()
  } catch {
    flash('save', t('common.saveFailed'), true)
  }
}

/** 把某个部位的建议路径加到指定通道的参数表。 */
async function addZone(zone: Zone, channel: 'a' | 'b') {
  try {
    const current = await api(`/api/v1/params/${channel}`)
    const params = (current.params ?? current.avatar_params ?? []) as any[]
    if (params.some(p => p.path === zone.suggested_path)) {
      flash('add', t('sps.alreadyAdded'), true)
      return
    }
    params.push({ path: zone.suggested_path, mode: 'distance', enabled: true })
    const data = await apiPost(`/api/v1/params/${channel}`, { params })
    flash('add', data.success ? t('sps.added') : t('common.saveFailed'), !data.success)
    if (data.success) await load()
  } catch {
    flash('add', t('common.saveFailed'), true)
  }
}

function relTime(ts: number) {
  const secs = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (secs < 2) return t('sps.justNow')
  if (secs < 60) return `${secs}s`
  return `${Math.floor(secs / 60)}m`
}

onMounted(() => {
  load()
  loadZones()
  zoneTimer = window.setInterval(loadZones, 2000)
})
onUnmounted(() => {
  if (zoneTimer !== undefined) window.clearInterval(zoneTimer)
})
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('sps.title') }}</h1>
    <p class="page-desc">{{ t('sps.desc') }}</p>

    <!-- 总开关 -->
    <section class="card">
      <h2>{{ t('sps.enableTitle') }}</h2>
      <label class="toggle-row">
        <input type="checkbox" v-model="enabled" />
        <span>{{ enabled ? t('common.enabled') : t('common.disabled') }}</span>
      </label>
      <p class="hint">{{ t('sps.enableHint') }}</p>
    </section>

    <!-- 部位发现 -->
    <section class="card">
      <h2>{{ t('sps.zonesTitle') }}</h2>
      <p class="page-desc" style="margin-bottom:var(--sp-4)">{{ t('sps.zonesDesc') }}</p>

      <div v-if="zones.length === 0" class="empty-box">
        {{ t('sps.zonesEmpty') }}
      </div>
      <table v-else class="zone-table">
        <thead>
          <tr>
            <th>{{ t('sps.colKind') }}</th>
            <th>{{ t('sps.colZone') }}</th>
            <th>{{ t('sps.colPath') }}</th>
            <th>{{ t('sps.colSeen') }}</th>
            <th>{{ t('sps.colAdd') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="z in zones" :key="z.trigger_type + z.zone_id">
            <td><span class="kind-tag" :class="z.kind.toLowerCase()">{{ z.kind }}</span></td>
            <td class="zone-id">{{ z.zone_id }}</td>
            <td class="path"><span class="path-prefix">/avatar/parameters/</span>{{ z.suggested_path.replace('/avatar/parameters/', '') }}</td>
            <td class="seen">{{ relTime(z.last_seen) }}</td>
            <td class="add-cell">
              <button class="btn btn-ghost btn-sm" @click="addZone(z, 'a')">A</button>
              <button class="btn btn-ghost btn-sm" @click="addZone(z, 'b')">B</button>
            </td>
          </tr>
        </tbody>
      </table>
      <span class="msg" :class="{ err: addMsgErr }">{{ addMsg }}</span>
    </section>

    <!-- 已配置的 OGB 参数 -->
    <section class="card">
      <h2>{{ t('sps.configuredTitle') }}</h2>
      <div class="configured-grid">
        <div v-for="ch in (['a', 'b'] as const)" :key="ch">
          <h3 class="ch-head">{{ t('sps.channel') }} {{ ch.toUpperCase() }}</h3>
          <p v-if="!ogbParams[ch] || ogbParams[ch].length === 0" class="hint">
            {{ t('sps.noneConfigured') }}
          </p>
          <ul v-else class="cfg-list">
            <li v-for="p in ogbParams[ch]" :key="p.path" :class="{ off: !p.enabled }">
              <code>{{ p.path.replace('/avatar/parameters/', '') }}</code>
              <span class="mode-tag">{{ p.mode }}</span>
            </li>
          </ul>
        </div>
      </div>
      <p class="hint">{{ t('sps.configuredHint') }}</p>
    </section>

    <!-- OGB_ENABLED 心跳 -->
    <section class="card">
      <h2>{{ t('sps.heartbeatTitle') }}</h2>
      <p class="page-desc" style="margin-bottom:var(--sp-4)">{{ t('sps.heartbeatDesc') }}</p>
      <div class="field">
        <label class="toggle-row">
          <input type="checkbox" v-model="heartbeat" />
          <span>{{ heartbeat ? t('common.enabled') : t('common.disabled') }}</span>
        </label>
      </div>
      <div class="osc-grid">
        <div class="field">
          <label>{{ t('sps.targetHost') }}</label>
          <input type="text" v-model="targetHost" />
        </div>
        <div class="field">
          <label>{{ t('sps.targetPort') }}</label>
          <input type="number" v-model.number="targetPort" min="1" max="65535" />
          <p class="hint">{{ t('sps.targetPortHint') }}</p>
        </div>
        <div class="field">
          <label>{{ t('sps.interval') }}</label>
          <input type="number" v-model.number="interval" step="1" min="1" max="60" />
          <p class="hint">{{ t('sps.intervalHint') }}</p>
        </div>
      </div>
    </section>

    <div class="save-bar">
      <button class="btn btn-primary" @click="save">{{ t('common.save') }}</button>
      <button class="btn btn-ghost" @click="load">{{ t('common.reload') }}</button>
      <span class="msg" :class="{ err: msgErr }">{{ msg }}</span>
    </div>

    <div class="info-card card">
      <h3>{{ t('sps.infoTitle') }}</h3>
      <ul>
        <li>{{ t('sps.info1') }}</li>
        <li>{{ t('sps.info2') }}</li>
        <li>{{ t('sps.info3') }}</li>
        <li>{{ t('sps.info4') }}</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.osc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: var(--sp-4);
}

.empty-box {
  padding: var(--sp-5);
  text-align: center;
  color: var(--text-dim);
  background: var(--bg-input, rgba(255, 255, 255, .03));
  border: 1px dashed var(--border);
  border-radius: var(--radius-md, 8px);
}

.zone-table {
  width: 100%;
  border-collapse: collapse;
}

.zone-table th {
  text-align: left;
  font-size: .78rem;
  font-weight: 600;
  color: var(--text-dim);
  padding: var(--sp-2) var(--sp-3);
  border-bottom: 1px solid var(--border);
}

.zone-table td {
  padding: var(--sp-3);
  border-bottom: 1px solid var(--border);
  font-size: .85rem;
}

.kind-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: .72rem;
  font-weight: 600;
  border: 1px solid var(--border);
}

.kind-tag.orf { color: #ff8bc8; border-color: #ff8bc855; }
.kind-tag.pen { color: #72c8e0; border-color: #72c8e055; }

.zone-id { font-weight: 600; }

.path { font-family: var(--font-mono, monospace); word-break: break-all; }
.path-prefix { color: var(--text-dim); opacity: .6; }

.seen { color: var(--text-dim); white-space: nowrap; }

.add-cell { white-space: nowrap; display: flex; gap: var(--sp-2); }

.btn-sm { padding: 2px 10px; font-size: .78rem; }

.configured-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: var(--sp-4);
  margin-bottom: var(--sp-3);
}

.ch-head {
  font-size: .9rem;
  margin: 0 0 var(--sp-2);
  color: var(--text-dim);
}

.cfg-list { list-style: none; margin: 0; padding: 0; }

.cfg-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-2);
  padding: var(--sp-2) 0;
  border-bottom: 1px solid var(--border);
}

.cfg-list li.off { opacity: .45; }

.cfg-list code {
  font-size: .8rem;
  word-break: break-all;
}

.mode-tag {
  font-size: .72rem;
  color: var(--text-dim);
  padding: 1px 7px;
  border: 1px solid var(--border);
  border-radius: 999px;
  white-space: nowrap;
}
</style>
