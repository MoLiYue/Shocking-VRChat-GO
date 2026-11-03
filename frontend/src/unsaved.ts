// Unsaved-changes tracking for forms with an explicit Save button.
//
// A page calls useUnsaved(() => <form state>) and then markClean() after
// loading and after a successful save. Leaving the route (or closing the
// window) while the state differs from the last clean snapshot asks first.
import { computed, onUnmounted, ref, type ComputedRef } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { confirmDialog } from '@/dialog'
import { useI18n } from '@/i18n'

const active = new Set<ComputedRef<boolean>>()

/** True if any mounted page has unsaved edits (used by beforeunload). */
export function anyUnsaved(): boolean {
  for (const d of active) if (d.value) return true
  return false
}

window.addEventListener('beforeunload', (e) => {
  if (anyUnsaved()) { e.preventDefault(); e.returnValue = '' }
})

export function useUnsaved(snapshot: () => unknown) {
  const { t } = useI18n()
  const base = ref<string | null>(null)
  const dirty = computed(() => base.value !== null && JSON.stringify(snapshot()) !== base.value)
  active.add(dirty)
  onUnmounted(() => active.delete(dirty))

  function markClean() { base.value = JSON.stringify(snapshot()) }

  /** Resolve true when it is fine to discard current edits. */
  async function confirmDiscard(): Promise<boolean> {
    if (!dirty.value) return true
    return confirmDialog({
      title: t('common.unsavedTitle'),
      message: t('common.unsavedBody'),
      confirmText: t('common.discard'),
      danger: true,
    })
  }

  onBeforeRouteLeave(() => confirmDiscard())
  return { dirty, markClean, confirmDiscard }
}
