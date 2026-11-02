<script setup lang="ts">
// Thin indeterminate bar shown only when a request takes noticeably long,
// so fast polling never flickers it.
import { ref, watch } from 'vue'
import { pendingRequests } from '@/api'

const visible = ref(false)
let showTimer: number | null = null

watch(pendingRequests, (n) => {
  if (n > 0 && !visible.value && showTimer === null) {
    showTimer = window.setTimeout(() => { visible.value = true; showTimer = null }, 350)
  } else if (n === 0) {
    if (showTimer !== null) { clearTimeout(showTimer); showTimer = null }
    visible.value = false
  }
})
</script>

<template>
  <Transition name="tp">
    <div v-if="visible" class="top-progress" role="progressbar" aria-busy="true"><span /></div>
  </Transition>
</template>

<style scoped>
.top-progress {
  position: fixed; top: 0; left: 0; right: 0; height: 2px; z-index: 1002;
  overflow: hidden; pointer-events: none;
}
.top-progress span {
  position: absolute; top: 0; bottom: 0; width: 30%;
  background: var(--gradient-btn);
  animation: tp-slide 1.1s cubic-bezier(0.4, 0, 0.2, 1) infinite;
}
@keyframes tp-slide { from { left: -30%; } to { left: 100%; } }
.tp-enter-active, .tp-leave-active { transition: opacity 200ms ease; }
.tp-enter-from, .tp-leave-to { opacity: 0; }
</style>
