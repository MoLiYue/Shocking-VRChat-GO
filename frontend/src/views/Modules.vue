<script setup lang="ts">
// Optional modules (integrations that are off by default). Each module is
// a self-contained card with its own enable switch.
import { useI18n } from '@/i18n'
import PulsoidSettings from '@/components/PulsoidSettings.vue'
import BleHeartRate from '@/components/BleHeartRate.vue'
import HypeRateSettings from '@/components/HypeRateSettings.vue'

const { t } = useI18n()
</script>

<template>
  <div>
    <h1 class="gradient-text page-title">{{ t('modules.title') }}</h1>
    <p class="page-desc">{{ t('modules.desc') }}</p>
    <div class="modules">
      <BleHeartRate />
      <HypeRateSettings />
      <PulsoidSettings />
    </div>
  </div>
</template>

<style scoped>
.modules { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 520px), 1fr)); gap: var(--sp-4); align-items: start; }
</style>
