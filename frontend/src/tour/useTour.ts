import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'

/** A single coachmark step. */
export interface TourStep {
  /** Stable step id (matches TOUR_SPEC route definition). */
  id: string
  /** data-tour attribute value to locate the anchor element (empty = centered). */
  target: string
  /** Route to navigate to before locating the anchor (optional). */
  route?: string
  /** i18n key for the step title. */
  title: string
  /** i18n key for the step body. */
  body: string
  /** Optional i18n key for a highlighted safety note. */
  safety?: string
  /** Optional i18n key for a safety-precautions link label. */
  safetyLink?: string
}

/**
 * Default onboarding route: params -> strength -> QR -> finish.
 * Anchor values MUST match the data-tour attributes placed in the views.
 */
export const TOUR_STEPS: TourStep[] = [
  { id: 'welcome', target: '', route: '/dashboard', title: 'tour.welcome.title', body: 'tour.welcome.body' },
  { id: 'goto-params', target: 'nav-params', title: 'tour.gotoParams.title', body: 'tour.gotoParams.body' },
  { id: 'add-param', target: 'param-add', route: '/params', title: 'tour.addParam.title', body: 'tour.addParam.body' },
  { id: 'pick-mode', target: 'param-mode-select', route: '/params', title: 'tour.pickMode.title', body: 'tour.pickMode.body' },
  { id: 'save-param', target: 'param-save', route: '/params', title: 'tour.saveParam.title', body: 'tour.saveParam.body' },
  { id: 'set-strength', target: 'strength-slider', route: '/params', title: 'tour.setStrength.title', body: 'tour.setStrength.body', safety: 'tour.setStrength.safety' },
  { id: 'goto-qr', target: 'nav-dashboard', title: 'tour.gotoQr.title', body: 'tour.gotoQr.body' },
  { id: 'scan-qr', target: 'qr-code', route: '/dashboard', title: 'tour.scanQr.title', body: 'tour.scanQr.body' },
  { id: 'finish', target: 'dashboard-status', route: '/dashboard', title: 'tour.finish.title', body: 'tour.finish.body', safety: 'tour.finish.safety', safetyLink: 'tour.finish.safetyLink' },
]

/** Rect of the currently highlighted element (viewport coordinates). */
export interface TargetRect {
  top: number
  left: number
  width: number
  height: number
}

const STORAGE_KEY = 'tour_done'
const POLL_INTERVAL_MS = 50
const POLL_TIMEOUT_MS = 1500

// Module-level singleton state so any component can drive/observe the tour.
const active = ref(false)
const index = ref(0)
const steps = ref<TourStep[]>([])
const targetRect = ref<TargetRect | null>(null)

// Generation token: incremented whenever the step changes or the tour ends.
// An in-flight resolveStep() captures the current value and bails out (without
// writing targetRect) once it observes a newer generation, so stale element
// polling can never overwrite the rect computed for the current step.
let resolveGeneration = 0

export function useTour() {
  const router = useRouter()

  const currentStep = computed<TourStep | null>(() => steps.value[index.value] ?? null)
  const total = computed(() => steps.value.length)
  const stepNumber = computed(() => index.value + 1)
  const isFirst = computed(() => index.value === 0)
  const isLast = computed(() => index.value >= steps.value.length - 1)

  /**
   * Wait until an element matching the selector is in the DOM, or timeout.
   * `isCurrent` lets the caller abort the poll early when the step changed:
   * the promise resolves with null so the caller can bail without writing rect.
   */
  function waitForElement(
    selector: string,
    isCurrent: () => boolean,
  ): Promise<HTMLElement | null> {
    return new Promise((resolve) => {
      const existing = document.querySelector<HTMLElement>(selector)
      if (existing) {
        resolve(existing)
        return
      }
      const start = performance.now()
      const timer = window.setInterval(() => {
        // Superseded by a newer step/restart/finish — stop polling immediately.
        if (!isCurrent()) {
          window.clearInterval(timer)
          resolve(null)
          return
        }
        const el = document.querySelector<HTMLElement>(selector)
        if (el) {
          window.clearInterval(timer)
          resolve(el)
        } else if (performance.now() - start >= POLL_TIMEOUT_MS) {
          window.clearInterval(timer)
          resolve(null)
        }
      }, POLL_INTERVAL_MS)
    })
  }

  /** Resolve the current step: navigate, locate the anchor, compute its rect. */
  async function resolveStep() {
    // Capture the generation for this resolution pass; any step change,
    // restart, finish or skip bumps resolveGeneration and invalidates us.
    const myGen = resolveGeneration
    const step = currentStep.value
    if (!step) return
    if (step.route && router.currentRoute.value.path !== step.route) {
      await router.push(step.route)
    }
    if (myGen !== resolveGeneration) return
    // Let the router-view render the new page before querying the DOM.
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    if (myGen !== resolveGeneration) return
    const el = await waitForElement(
      `[data-tour="${step.target}"]`,
      () => myGen === resolveGeneration,
    )
    // A newer resolution superseded us while we were polling — do not write,
    // otherwise a stale hit/timeout would clobber the current step's rect.
    if (myGen !== resolveGeneration) return
    if (el) {
      // Targets can be further down a long page (combined setup page).
      el.scrollIntoView({ block: 'center', behavior: 'instant' as ScrollBehavior })
      await new Promise<void>((r) => requestAnimationFrame(() => r()))
      if (myGen !== resolveGeneration) return
      const rect = el.getBoundingClientRect()
      targetRect.value = {
        top: rect.top,
        left: rect.left,
        width: rect.width,
        height: rect.height,
      }
    } else {
      // Anchor missing/unrendered: don't block, just center the bubble.
      targetRect.value = null
    }
  }

  /** Recompute the current target rect (e.g. on window resize). */
  function recompute() {
    const step = currentStep.value
    if (!step) return
    const el = document.querySelector<HTMLElement>(`[data-tour="${step.target}"]`)
    if (el) {
      const rect = el.getBoundingClientRect()
      targetRect.value = {
        top: rect.top,
        left: rect.left,
        width: rect.width,
        height: rect.height,
      }
    } else {
      targetRect.value = null
    }
  }

  /** Invalidate any in-flight resolveStep() so it won't write a stale rect. */
  function invalidate() {
    resolveGeneration += 1
  }

  async function next() {
    if (isLast.value) {
      finish()
      return
    }
    invalidate()
    index.value += 1
    await resolveStep()
  }

  async function prev() {
    if (isFirst.value) return
    invalidate()
    index.value -= 1
    await resolveStep()
  }

  function skip() {
    finish()
  }

  function finish() {
    // Cancel any pending resolution so it can't reset targetRect after we end.
    invalidate()
    active.value = false
    targetRect.value = null
    try {
      localStorage.setItem(STORAGE_KEY, '1')
    } catch {
      // ignore storage errors (private mode etc.)
    }
  }

  function hasCompleted(): boolean {
    try {
      return localStorage.getItem(STORAGE_KEY) === '1'
    } catch {
      return false
    }
  }

  /** Start the tour if it has never been completed. Defaults to TOUR_STEPS. */
  async function start(tourSteps: TourStep[] = TOUR_STEPS) {
    if (hasCompleted()) return
    await run(tourSteps)
  }

  /** Force-start the tour regardless of completion state. Defaults to TOUR_STEPS. */
  async function restart(tourSteps: TourStep[] = TOUR_STEPS) {
    await run(tourSteps)
  }

  async function run(tourSteps: TourStep[]) {
    // A restart while a previous pass is resolving must not let the old poll
    // write a rect for the wrong step.
    invalidate()
    steps.value = tourSteps
    index.value = 0
    active.value = true
    await resolveStep()
  }

  return {
    active,
    index,
    steps,
    targetRect,
    currentStep,
    total,
    stepNumber,
    isFirst,
    isLast,
    next,
    prev,
    skip,
    finish,
    recompute,
    start,
    restart,
    hasCompleted,
  }
}
