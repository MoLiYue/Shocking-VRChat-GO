<script setup lang="ts">
// Combined setup page: strength limits -> parameter mapping -> overlimit
// rules (in that order). Each section is the former page, embedded; the
// old /strength and /overlimit-rules routes redirect to #strength / #overlimit.
import { nextTick, onActivated, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Gauge, SlidersHorizontal, ShieldAlert } from 'lucide-vue-next'
import { useI18n } from '@/i18n'
import StrengthLimit from './StrengthLimit.vue'
import ParamMap from './ParamMap.vue'
import OverlimitRules from './OverlimitRules.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const SECTIONS = [
  { id: 'strength', title: 'strength.title', desc: 'strength.desc', icon: Gauge },
  { id: 'params', title: 'params.title', desc: 'params.desc', icon: SlidersHorizontal },
  { id: 'overlimit', title: 'overlimit.title', desc: 'overlimit.desc', icon: ShieldAlert },
] as const
const active = ref<string>('strength')

function go(id: string, smooth = true) {
  const el = document.getElementById('setup-' + id)
  el?.scrollIntoView({ block: 'start', behavior: smooth ? 'smooth' : ('instant' as ScrollBehavior) })
  active.value = id
  if (route.hash !== '#' + id) router.replace({ hash: '#' + id })
}

async function followHash() {
  const id = route.hash.replace('#', '')
  if (SECTIONS.some((s) => s.id === id)) { await nextTick(); go(id, false) }
}
watch(() => route.hash, followHash)
onMounted(followHash)
onActivated(followHash)

// Highlight the section in view.
let io: IntersectionObserver | null = null
onMounted(() => {
  io = new IntersectionObserver((entries) => {
    for (const e of entries) if (e.isIntersecting) active.value = e.target.id.replace('setup-', '')
  }, { rootMargin: '-20% 0px -70% 0px' })
  for (const s of SECTIONS) { const el = document.getElementById('setup-' + s.id); if (el) io.observe(el) }
})
onUnmounted(() => io?.disconnect())
</script>

<template>
  <div class="setup">
    <nav class="section-nav" :aria-label="t('nav.setup')">
      <button v-for="(s, i) in SECTIONS" :key="s.id" type="button" :class="{ active: active === s.id }"
        :aria-current="active === s.id ? 'true' : undefined" @click="go(s.id)">
        <span class="num">{{ i + 1 }}</span><component :is="s.icon" :size="15" />{{ t(s.title) }}
      </button>
    </nav>

    <section v-for="s in SECTIONS" :id="'setup-' + s.id" :key="s.id" class="setup-section">
      <h1 class="gradient-text page-title">{{ t(s.title) }}</h1>
      <p class="page-desc">{{ t(s.desc) }}</p>
      <StrengthLimit v-if="s.id === 'strength'" embedded />
      <ParamMap v-else-if="s.id === 'params'" embedded />
      <OverlimitRules v-else embedded />
    </section>
  </div>
</template>

<style scoped>
.section-nav {
  position: sticky; top: 0; z-index: 20;
  display: flex; gap: var(--sp-2); flex-wrap: wrap;
  padding: var(--sp-2) 0 var(--sp-3);
  background: var(--bg-base);
  border-bottom: 1px solid var(--border);
  margin-bottom: var(--sp-4);
}
.section-nav button {
  display: inline-flex; align-items: center; gap: 6px;
  padding: var(--sp-2) var(--sp-4);
  border: 1px solid var(--border); border-radius: var(--radius-full);
  background: transparent; color: var(--text-muted); cursor: pointer;
  font-size: var(--text-sm); transition: var(--transition-props);
}
.section-nav button:hover { border-color: var(--border-hover); color: var(--text); }
.section-nav button.active { border-color: var(--accent); color: var(--text); background: var(--bg-active); }
.num {
  display: inline-grid; place-items: center; width: 18px; height: 18px; border-radius: 50%;
  background: var(--bg-tertiary); font-size: var(--text-2xs); font-weight: 700;
}
.section-nav button.active .num { background: var(--accent); color: var(--bg-base); }
.setup-section { scroll-margin-top: 64px; }
.setup-section + .setup-section { margin-top: var(--sp-8, 48px); padding-top: var(--sp-6); border-top: 1px dashed var(--border); }
</style>
