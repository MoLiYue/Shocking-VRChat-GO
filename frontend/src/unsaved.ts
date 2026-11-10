// Unsaved-changes tracking, optionally with auto-save.
//
// A page calls useUnsaved(() => <form state>) and then markClean() after
// loading and after a successful save. Leaving the route (or closing the
// window) while the state differs from the last clean snapshot asks first.
//
// With { save } the page saves itself: changes are saved `delay` ms after
// the last edit (debounced). Leaving the page or switching channel first
// flushes a pending save; only if that save fails does it ask. Loaders call
// markLoading() before fetching so the half-loaded state is never saved.
import { computed, onUnmounted, ref, watch, type ComputedRef } from 'vue'
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

export type AutoSaveState = 'idle' | 'pending' | 'saving' | 'saved' | 'error'

export interface AutoSaveOptions {
  /** Saves the current state; must call markClean() on success. */
  save: () => Promise<unknown>
  /** Debounce after the last change (ms). */
  delay?: number
  /** While true, changes are kept dirty but not saved (e.g. invalid input). */
  blocked?: () => boolean
}

export function useUnsaved(snapshot: () => unknown, auto?: AutoSaveOptions) {
  const { t } = useI18n()
  const base = ref<string | null>(null)
  const current = computed(() => JSON.stringify(snapshot()))
  const dirty = computed(() => base.value !== null && current.value !== base.value)
  active.add(dirty)

  const state = ref<AutoSaveState>('idle')
  let timer = 0
  let running: Promise<void> | null = null

  function markClean() { base.value = JSON.stringify(snapshot()) }
  /** The form is being (re)loaded: nothing to save until markClean(). */
  function markLoading() { clearTimeout(timer); base.value = null }

  async function run() {
    clearTimeout(timer)
    if (!auto || !dirty.value || auto.blocked?.()) {
      if (state.value === 'pending') state.value = dirty.value ? 'idle' : 'saved'
      return
    }
    if (running) { await running; return run() }
    state.value = 'saving'
    const before = current.value
    running = (async () => {
      try { await auto.save() } catch { /* reported below */ }
    })()
    await running
    running = null
    if (!dirty.value) state.value = 'saved'
    else if (current.value !== before) schedule() // edited while saving
    else state.value = 'error'
  }

  function schedule() {
    clearTimeout(timer)
    if (!auto || !dirty.value) return
    state.value = 'pending'
    timer = window.setTimeout(run, auto.delay ?? 600)
  }

  /** Save now if there is anything pending (resolves when done). */
  async function flush() {
    if (auto && dirty.value) await run()
    else if (running) await running
  }

  if (auto) watch(current, () => schedule())

  /** Resolve true when it is fine to leave / discard current edits. */
  async function confirmDiscard(): Promise<boolean> {
    if (auto) await flush()
    if (!dirty.value) return true
    return confirmDialog({
      title: t('common.unsavedTitle'),
      message: t('common.unsavedBody'),
      confirmText: t('common.discard'),
      danger: true,
    })
  }

  onBeforeRouteLeave(() => confirmDiscard())
  onUnmounted(() => {
    active.delete(dirty)
    if (auto && dirty.value) void run() // best effort when the view goes away
  })
  return { dirty, markClean, markLoading, confirmDiscard, flush, state }
}
