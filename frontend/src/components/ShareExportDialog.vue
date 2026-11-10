<script setup lang="ts">
// Export: share card image (with embedded data), .svrc file, share code.
import { onUnmounted, ref, watch } from 'vue'
import { Image as ImageIcon, FileArchive, Copy, X } from 'lucide-vue-next'
import { api } from '@/api'
import { useI18n } from '@/i18n'
import { toast } from '@/dialog'
import { shareState, renderShareCard, downloadBlob, dispositionName, type ShareDoc } from '@/share'

const { t } = useI18n()
const title = ref(localStorage.getItem('shareTitle') || '')
const note = ref(localStorage.getItem('shareNote') || '')
const withAvatar = ref(false)
const doc = ref<ShareDoc | null>(null)
const cardUrl = ref('')
let cardBlob: Blob | null = null
const busy = ref(false)
const code = ref('')

function modeLabel(m: string) {
  const key = 'params.mode' + m.charAt(0).toUpperCase() + m.slice(1)
  const tr = t(key)
  return tr !== key ? tr : m
}
function query() {
  return `title=${encodeURIComponent(title.value)}&note=${encodeURIComponent(note.value)}&avatar=${withAvatar.value ? 1 : 0}`
}

let timer = 0
async function refresh() {
  localStorage.setItem('shareTitle', title.value)
  localStorage.setItem('shareNote', note.value)
  try {
    doc.value = await api('/api/v1/share/data?' + query())
    cardBlob = await renderShareCard(doc.value!, t, modeLabel)
    if (cardUrl.value) URL.revokeObjectURL(cardUrl.value)
    cardUrl.value = URL.createObjectURL(cardBlob)
    code.value = ''
  } catch { toast(t('share.failed'), 'error') }
}
watch(() => shareState.exportOpen, (o) => { if (o) refresh() })
watch([title, note, withAvatar], () => { clearTimeout(timer); timer = window.setTimeout(refresh, 400) })
onUnmounted(() => { if (cardUrl.value) URL.revokeObjectURL(cardUrl.value) })

async function save(kind: 'png' | 'svrc') {
  if (!cardBlob) return
  busy.value = true
  try {
    const fd = new FormData()
    fd.append('image', cardBlob, 'card.png')
    fd.append('title', title.value)
    fd.append('note', note.value)
    fd.append('avatar', withAvatar.value ? '1' : '0')
    const resp = await fetch(`/api/v1/share/${kind}`, { method: 'POST', body: fd })
    if (!resp.ok) throw new Error(String(resp.status))
    downloadBlob(await resp.blob(), dispositionName(resp, `share.${kind}`))
    toast(t(kind === 'png' ? 'share.savedPng' : 'share.savedSvrc'), 'success')
  } catch { toast(t('share.failed'), 'error') } finally { busy.value = false }
}

async function copyCode() {
  try {
    const d = await api('/api/v1/share/code?' + query())
    code.value = d.code
    await navigator.clipboard.writeText(d.code)
    toast(t('share.codeCopied', { n: d.code.length }), 'success')
  } catch { if (code.value) toast(t('share.codeSelect'), 'info'); else toast(t('share.failed'), 'error') }
}
</script>

<template>
  <div v-if="shareState.exportOpen" class="dlg-backdrop" @mousedown.self="shareState.exportOpen = false">
    <div class="dlg share-dlg" role="dialog" aria-modal="true" :aria-label="t('share.exportTitle')">
      <div class="head">
        <h3>{{ t('share.exportTitle') }}</h3>
        <button class="icon-action" :aria-label="t('common.cancel')" @click="shareState.exportOpen = false"><X :size="16" /></button>
      </div>
      <div class="form">
        <label>{{ t('share.title') }}<input v-model="title" maxlength="60" :placeholder="t('share.cardDefaultTitle')"></label>
        <label>{{ t('share.note') }}<input v-model="note" maxlength="300"></label>
        <label class="chk"><input type="checkbox" v-model="withAvatar">{{ t('share.includeAvatar') }}</label>
        <p class="hint">{{ t('share.privacyHint') }}</p>
      </div>
      <div class="card-preview"><img v-if="cardUrl" :src="cardUrl" :alt="t('share.exportTitle')"></div>
      <div class="actions">
        <button class="btn btn-primary" :disabled="busy || !cardUrl" @click="save('png')"><ImageIcon :size="15" />{{ t('share.savePng') }}</button>
        <button class="btn btn-ghost" :disabled="busy || !cardUrl" @click="save('svrc')"><FileArchive :size="15" />{{ t('share.saveSvrc') }}</button>
        <button class="btn btn-ghost" :disabled="busy" @click="copyCode"><Copy :size="15" />{{ t('share.copyCode') }}</button>
      </div>
      <textarea v-if="code" class="code" readonly :value="code" @focus="($event.target as HTMLTextAreaElement).select()"></textarea>
      <p class="hint">{{ t('share.formatsHint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.dlg-backdrop { position: fixed; inset: 0; z-index: 900; display: grid; place-items: center; background: var(--overlay); }
.share-dlg { width: min(760px, calc(100vw - 32px)); max-height: calc(100vh - 48px); overflow: auto; background: var(--bg-popover); border: 1px solid var(--border-hover); border-radius: var(--radius-lg); padding: var(--sp-5); box-shadow: var(--shadow-pop); }
.head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--sp-3); }
.head h3 { margin: 0; font-size: var(--text-lg); }
.form { display: grid; grid-template-columns: 1fr 1fr; gap: var(--sp-3); align-items: end; }
.form label { display: flex; flex-direction: column; gap: 4px; font-size: var(--text-sm); color: var(--text-label); }
.form label.chk { flex-direction: row; align-items: center; gap: var(--sp-2); }
.hint { font-size: var(--text-xs); color: var(--text-muted); margin: 0; }
.card-preview { margin: var(--sp-4) 0; border-radius: var(--radius-md); overflow: hidden; background: var(--bg-inset); min-height: 80px; }
.card-preview img { display: block; width: 100%; }
.actions { display: flex; flex-wrap: wrap; gap: var(--sp-2); margin-bottom: var(--sp-2); }
.code { width: 100%; height: 72px; font-family: var(--font-mono); font-size: var(--text-2xs); margin-bottom: var(--sp-2); }
</style>
