<script setup lang="ts">
import { nextTick, ref, watch, onMounted, onUnmounted } from 'vue'
import { dialogState, closeConfirm } from '@/dialog'
import { useI18n } from '@/i18n'

const { t } = useI18n()
const confirmBtn = ref<HTMLButtonElement | null>(null)

// Focus the primary action when the dialog opens (keyboard friendly).
watch(() => dialogState.confirm.open, async (open) => {
  if (open) {
    await nextTick()
    confirmBtn.value?.focus()
  }
})

function onKey(e: KeyboardEvent) {
  if (!dialogState.confirm.open) return
  if (e.key === 'Escape') { e.preventDefault(); closeConfirm(false) }
  if (e.key === 'Enter') { e.preventDefault(); closeConfirm(true) }
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <Transition name="dlg">
      <div v-if="dialogState.confirm.open" class="dlg-backdrop" @mousedown.self="closeConfirm(false)">
        <div class="dlg" role="alertdialog" aria-modal="true" :aria-label="dialogState.confirm.title || t('common.confirm')">
          <h3 v-if="dialogState.confirm.title" class="dlg-title">{{ dialogState.confirm.title }}</h3>
          <p class="dlg-msg">{{ dialogState.confirm.message }}</p>
          <div class="dlg-actions">
            <button class="btn btn-ghost" @click="closeConfirm(false)">
              {{ dialogState.confirm.cancelText || t('common.cancel') }}
            </button>
            <button
              ref="confirmBtn"
              class="btn"
              :class="dialogState.confirm.danger ? 'btn-danger' : 'btn-primary'"
              @click="closeConfirm(true)"
            >
              {{ dialogState.confirm.confirmText || t('common.confirm') }}
            </button>
          </div>
        </div>
      </div>
    </Transition>

    <div class="toasts" aria-live="polite">
      <TransitionGroup name="toast">
        <div v-for="tst in dialogState.toasts" :key="tst.id" class="toast" :class="'toast-' + tst.type">
          {{ tst.message }}
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.dlg-backdrop {
  position: fixed; inset: 0; z-index: 1000;
  display: grid; place-items: center;
  background: var(--overlay);
}
.dlg {
  width: min(420px, calc(100vw - 32px));
  background: var(--bg-popover);
  border: 1px solid var(--border-hover);
  border-radius: var(--radius-lg);
  padding: var(--sp-6);
  box-shadow: var(--shadow-pop);
}
.dlg-title { font-size: var(--text-lg); font-weight: 700; margin-bottom: var(--sp-2); }
.dlg-msg { color: var(--text-secondary); white-space: pre-wrap; line-height: 1.6; }
.dlg-actions { display: flex; justify-content: flex-end; gap: var(--sp-3); margin-top: var(--sp-6); }

.dlg-enter-active, .dlg-leave-active { transition: opacity 140ms ease; }
.dlg-enter-active .dlg, .dlg-leave-active .dlg { transition: transform 160ms cubic-bezier(0.2, 0.8, 0.2, 1), opacity 140ms ease; }
.dlg-enter-from, .dlg-leave-to { opacity: 0; }
.dlg-enter-from .dlg, .dlg-leave-to .dlg { transform: scale(0.96); opacity: 0; }

.toasts {
  /* Sits above the sticky save bar (~64px tall) so it never covers it */
  position: fixed; right: var(--sp-6); bottom: 96px; z-index: 1001;
  display: flex; flex-direction: column; gap: var(--sp-2); align-items: flex-end;
  pointer-events: none;
}
.toast {
  max-width: 360px;
  padding: var(--sp-3) var(--sp-4);
  border-radius: var(--radius-md);
  background: var(--bg-toast);
  border: 1px solid var(--border-hover);
  color: var(--text);
  font-size: var(--text-sm);
  box-shadow: var(--shadow-float);
}
.toast-success { border-color: rgba(var(--green-rgb), 0.45); }
.toast-error { border-color: rgba(var(--red-rgb), 0.55); }
.toast-enter-active, .toast-leave-active { transition: opacity 180ms ease, transform 180ms ease; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateY(8px); }
</style>
