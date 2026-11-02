<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, watch, ref } from 'vue'
import { useI18n } from '@/i18n'
import { useTour } from './useTour'

const { t } = useI18n()
const {
  active, targetRect, currentStep, total, stepNumber,
  isFirst, isLast, next, prev, skip, recompute,
} = useTour()

// Highlight box inset (--sp-2 = 8px) and safe margin from viewport edges.
const PAD = 8
const MARGIN = 12
const BUBBLE_W = 320
const BUBBLE_GAP = 14

const bubbleRef = ref<HTMLElement | null>(null)

/** Highlight cutout box (padded target rect), or null when no anchor. */
const highlight = computed(() => {
  const r = targetRect.value
  if (!r) return null
  return {
    top: r.top - PAD,
    left: r.left - PAD,
    width: r.width + PAD * 2,
    height: r.height + PAD * 2,
  }
})

interface BubblePos { top: number; left: number }

/** Position the bubble around the target, auto-avoiding viewport edges. */
const bubblePos = computed<BubblePos>(() => {
  const vw = window.innerWidth
  const vh = window.innerHeight
  const bh = bubbleRef.value?.offsetHeight ?? 160
  const bw = bubbleRef.value?.offsetWidth ?? BUBBLE_W
  const hl = highlight.value

  // No anchor: center on screen.
  if (!hl) {
    return {
      top: Math.max(MARGIN, (vh - bh) / 2),
      left: Math.max(MARGIN, (vw - bw) / 2),
    }
  }

  const clampLeft = (l: number) => Math.min(Math.max(MARGIN, l), vw - bw - MARGIN)
  const clampTop = (tp: number) => Math.min(Math.max(MARGIN, tp), vh - bh - MARGIN)

  // Try bottom -> top -> right -> left, pick the first that fits the viewport.
  // bottom
  if (hl.top + hl.height + BUBBLE_GAP + bh <= vh - MARGIN) {
    return {
      top: hl.top + hl.height + BUBBLE_GAP,
      left: clampLeft(hl.left + hl.width / 2 - bw / 2),
    }
  }
  // top
  if (hl.top - BUBBLE_GAP - bh >= MARGIN) {
    return {
      top: hl.top - BUBBLE_GAP - bh,
      left: clampLeft(hl.left + hl.width / 2 - bw / 2),
    }
  }
  // right
  if (hl.left + hl.width + BUBBLE_GAP + bw <= vw - MARGIN) {
    return {
      top: clampTop(hl.top + hl.height / 2 - bh / 2),
      left: hl.left + hl.width + BUBBLE_GAP,
    }
  }
  // left
  if (hl.left - BUBBLE_GAP - bw >= MARGIN) {
    return {
      top: clampTop(hl.top + hl.height / 2 - bh / 2),
      left: hl.left - BUBBLE_GAP - bw,
    }
  }
  // Nothing fits: center fallback.
  return {
    top: Math.max(MARGIN, (vh - bh) / 2),
    left: Math.max(MARGIN, (vw - bw) / 2),
  }
})

function onResize() {
  recompute()
}

onMounted(() => {
  window.addEventListener('resize', onResize)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
})

// Recompute after each step change once the DOM settles.
watch([active, stepNumber], () => {
  if (active.value) {
    requestAnimationFrame(() => recompute())
  }
})
</script>

<template>
  <div v-if="active && currentStep" class="tour-root">
    <!-- Mask with cutout highlight (box-shadow spread creates the dimmed area) -->
    <div
      v-if="highlight"
      class="tour-highlight"
      :style="{
        top: highlight.top + 'px',
        left: highlight.left + 'px',
        width: highlight.width + 'px',
        height: highlight.height + 'px',
      }"
    ></div>
    <!-- Full-screen dim when there is no anchor to highlight -->
    <div v-else class="tour-mask-full"></div>

    <!-- Coachmark bubble -->
    <div
      ref="bubbleRef"
      class="tour-bubble card"
      :style="{ top: bubblePos.top + 'px', left: bubblePos.left + 'px' }"
    >
      <div class="tour-title gradient-text">{{ t(currentStep.title) }}</div>
      <div class="tour-body">{{ t(currentStep.body) }}</div>
      <div v-if="currentStep.safety" class="tour-safety">
        {{ t(currentStep.safety) }}
        <a
          v-if="currentStep.safetyLink"
          class="tour-safety-link"
          href="https://github.com/MoLiYue/Shocking-VRChat/blob/main/doc/dglab/%E5%AE%89%E5%85%A8%E9%A1%BB%E7%9F%A5.md"
          target="_blank"
          rel="noopener noreferrer"
        >{{ t(currentStep.safetyLink) }}</a>
      </div>
      <div class="tour-footer">
        <span class="tour-progress">{{ t('tour.progress', { n: stepNumber, total }) }}</span>
        <div class="tour-actions">
          <button class="btn btn-ghost btn-sm" @click="skip">{{ t('tour.skip') }}</button>
          <button v-if="!isFirst" class="btn btn-ghost" @click="prev">{{ t('tour.prev') }}</button>
          <button class="btn btn-primary" @click="next">
            {{ isLast ? t('tour.done') : t('tour.next') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tour-root {
  position: fixed;
  inset: 0;
  z-index: 9998;
  pointer-events: none;
}

/* Cutout highlight: huge box-shadow spreads the dim over the whole viewport,
   leaving the box itself transparent. */
.tour-highlight {
  position: fixed;
  border-radius: var(--radius-md);
  box-shadow: 0 0 0 9999px rgba(0, 0, 0, 0.6);
  border: 1px solid var(--border-hover);
  transition: top 200ms ease, left 200ms ease, width 200ms ease, height 200ms ease;
  pointer-events: none;
}

.tour-mask-full {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  pointer-events: none;
}

.tour-bubble {
  position: fixed;
  width: 320px;
  max-width: calc(100vw - 24px);
  padding: var(--sp-5);
  z-index: 9999;
  pointer-events: auto;
  box-shadow: var(--shadow), var(--glow-md);
  transition: top 200ms ease, left 200ms ease;
}

.tour-title {
  font-size: var(--text-lg);
  font-weight: 800;
  margin-bottom: var(--sp-2);
}
.tour-body {
  font-size: var(--text-sm);
  color: var(--text-secondary);
  line-height: 1.6;
  margin-bottom: var(--sp-4);
}
.tour-safety {
  font-size: var(--text-xs);
  color: var(--warning);
  line-height: 1.6;
  background: var(--warning-surface);
  border: 1px solid var(--warning-border);
  border-radius: var(--radius-md);
  padding: var(--sp-2) var(--sp-3);
  margin-bottom: var(--sp-4);
}
.tour-safety-link {
  color: var(--warning);
  text-decoration: underline;
  font-weight: 600;
}
.tour-safety-link:hover { color: var(--text); }
.tour-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-3);
}
.tour-progress {
  font-size: var(--text-xs);
  color: var(--text-muted);
  font-weight: 600;
  white-space: nowrap;
}
.tour-actions {
  display: flex;
  align-items: center;
  gap: var(--sp-2);
}
</style>
