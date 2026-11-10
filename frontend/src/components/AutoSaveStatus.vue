<script setup lang="ts">
// Shows the auto-save state of a form (see useUnsaved({ save })).
import { Check, LoaderCircle, CircleAlert } from 'lucide-vue-next'
import { onMounted, onUnmounted } from 'vue'
import { useI18n } from '@/i18n'
import type { AutoSaveState } from '@/unsaved'

defineProps<{ state: AutoSaveState }>()
const emit = defineEmits<{ (e: 'retry'): void }>()
const { t } = useI18n()

// Ctrl+S on an auto-saving page saves immediately (see App.vue).
const flushNow = () => emit('retry')
onMounted(() => window.addEventListener('autosave-flush', flushNow))
onUnmounted(() => window.removeEventListener('autosave-flush', flushNow))
</script>

<template>
  <span class="autosave" :class="state" role="status" aria-live="polite">
    <template v-if="state === 'pending' || state === 'saving'">
      <LoaderCircle :size="13" class="spin" />{{ t('common.autoSaving') }}
    </template>
    <template v-else-if="state === 'error'">
      <CircleAlert :size="13" />{{ t('common.autoSaveFailed') }}
      <button type="button" class="link" @click="$emit('retry')">{{ t('common.retry') }}</button>
    </template>
    <template v-else>
      <Check :size="13" />{{ t('common.autoSaved') }}
    </template>
  </span>
</template>

<style scoped>
.autosave { display: inline-flex; align-items: center; gap: 4px; font-size: var(--text-xs); color: var(--text-muted); }
.autosave.saved { color: var(--success); }
.autosave.error { color: var(--danger); }
.link { background: none; border: none; padding: 0; margin-left: 4px; color: inherit; text-decoration: underline; cursor: pointer; font-size: inherit; }
.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
