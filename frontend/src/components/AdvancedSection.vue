<script setup lang="ts">
// Collapsible "Advanced" block used by the mode pages (collapsed by default).
import { ref } from 'vue'
import { ChevronRight } from 'lucide-vue-next'
import { useI18n } from '@/i18n'

defineProps<{ title?: string }>()
const { t } = useI18n()
const open = ref(false)
const id = `adv-${Math.random().toString(36).slice(2, 8)}`
</script>

<template>
  <div class="advanced">
    <button type="button" class="btn btn-ghost btn-sm toggle" :aria-expanded="open" :aria-controls="id" @click="open = !open">
      <ChevronRight :size="14" class="chev" :class="{ open }" />
      {{ title || t('common.advanced') }}
    </button>
    <div v-show="open" :id="id" class="body">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.advanced { margin-top: var(--sp-4); }
.toggle { display: inline-flex; align-items: center; gap: var(--sp-1); }
.chev { transition: transform 0.15s; }
.chev.open { transform: rotate(90deg); }
.body { margin-top: var(--sp-3); }
</style>
