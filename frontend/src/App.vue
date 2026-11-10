<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  LayoutDashboard, SlidersHorizontal, Plug, Gamepad2, Zap, Ruler, Hand,
  Shuffle, FlaskConical, ScrollText, Disc, Activity, Bug, MessageSquare, Settings, Languages, Power,
  PanelLeftClose, PanelLeftOpen, ChevronDown, Sun, Moon,
} from 'lucide-vue-next'
import { useI18n } from '@/i18n'
import TourOverlay from '@/tour/TourOverlay.vue'
import AppDialog from '@/components/AppDialog.vue'
import ShareExportDialog from '@/components/ShareExportDialog.vue'
import ShareImportDialog from '@/components/ShareImportDialog.vue'
import { previewShare } from '@/share'
import TopProgress from '@/components/TopProgress.vue'
import { appStatus, startStatusPolling } from '@/status'
import { confirmDialog } from '@/dialog'
import { useTour } from '@/tour/useTour'
import { effectiveTheme, setTheme } from '@/theme'

const { t, locale, setLocale } = useI18n()
const { start } = useTour()

// Sidebar collapsed state survives reloads.
const sidebarPref = ref(localStorage.getItem('sidebar_open') !== '0')
watch(sidebarPref, (v) => localStorage.setItem('sidebar_open', v ? '1' : '0'))
// Narrow windows / phones always get the icon rail.
const narrowMq = window.matchMedia('(max-width: 768px)')
const narrow = ref(narrowMq.matches)
narrowMq.addEventListener('change', (e) => { narrow.value = e.matches })
const sidebarOpen = computed(() => sidebarPref.value && !narrow.value)

interface NavItem {
  path?: string
  key?: string
  label: string
  icon: Component
  children?: NavItem[]
}

// Modes group is identified by its i18n key so the expanded state does not
// depend on the current language.
const MODES_GROUP_KEY = 'nav.modes'

const DEBUG_GROUP_KEY = 'nav.debug'

const navItems = computed<NavItem[]>(() => [
  { path: '/dashboard', label: t('nav.dashboard'), icon: LayoutDashboard },
  { path: '/params', label: t('nav.setup'), icon: SlidersHorizontal },
  { path: '/sps', label: t('nav.sps'), icon: Plug },
  { path: '/chatbox', label: t('nav.chatbox'), icon: MessageSquare },
  {
    key: MODES_GROUP_KEY, label: t('nav.modes'), icon: Gamepad2,
    children: [
      { path: '/mode/shock', label: t('nav.modeShock'), icon: Zap },
      { path: '/mode/distance', label: t('nav.modeDistance'), icon: Ruler },
      { path: '/mode/touch', label: t('nav.modeTouch'), icon: Hand },
      { path: '/mode/combo', label: t('nav.modeCombo'), icon: Shuffle },
    ],
  },
  {
    // tools for testing / inspecting rather than day-to-day setup
    key: DEBUG_GROUP_KEY, label: t('nav.debug'), icon: Bug,
    children: [
      { path: '/wave-test', label: t('nav.waveTest'), icon: FlaskConical },
      { path: '/recorder', label: t('nav.recorder'), icon: Disc },
      { path: '/monitor', label: t('nav.monitor'), icon: Activity },
    ],
  },
  { path: '/logs', label: t('nav.logs'), icon: ScrollText },
  { path: '/settings', label: t('nav.settings'), icon: Settings },
])

const expandedGroups = ref<Set<string>>(new Set([MODES_GROUP_KEY]))
// The group holding the current page is always expanded (debug starts collapsed).
const route = useRoute()
watch(() => route.path, (p) => {
  for (const item of navItems.value) {
    if (item.key && item.children?.some((c) => c.path === p) && !expandedGroups.value.has(item.key)) {
      expandedGroups.value = new Set([...expandedGroups.value, item.key])
    }
  }
}, { immediate: true })

// data-tour anchor contract: map top-level nav paths to stable tour anchors.
const TOUR_ANCHORS: Record<string, string> = {
  '/dashboard': 'nav-dashboard',
  '/params': 'nav-params',
}

function toggleGroup(key: string) {
  const s = new Set(expandedGroups.value)
  if (s.has(key)) s.delete(key)
  else s.add(key)
  expandedGroups.value = s
}

function toggleTheme() {
  setTheme(effectiveTheme.value === 'dark' ? 'light' : 'dark')
}

function toggleLocale() {
  setLocale(locale.value === 'zh' ? 'en' : 'zh')
}

async function shutdownApp() {
  const ok = await confirmDialog({
    message: t('nav.shutdownConfirm'), danger: true, confirmText: t('nav.shutdown'),
  })
  if (!ok) return
  try {
    await fetch('/api/v1/shutdown', { method: 'POST' })
  } catch {}
}

const statusText = computed(() => {
  if (!appStatus.reachable) return t('statusbar.offline')
  if (appStatus.devices > 0) return t('statusbar.connected', { count: appStatus.devices })
  return t('statusbar.disconnected')
})
const statusKind = computed(() => (!appStatus.reachable ? 'err' : appStatus.devices > 0 ? 'ok' : 'idle'))

// Keyboard shortcuts: Ctrl+S saves the current page, Ctrl+1..9 switch pages.
const router = useRouter()
const flatPaths = computed(() => navItems.value.flatMap((i) => (i.children ? i.children : [i])).map((i) => i.path!))
function onKey(e: KeyboardEvent) {
  if (!(e.ctrlKey || e.metaKey) || e.altKey) return
  if (document.querySelector('.dlg-backdrop')) return // a dialog owns the keyboard
  if (e.key === 's' || e.key === 'S') {
    e.preventDefault()
    const btn = document.querySelector<HTMLButtonElement>('.main .save-bar .btn-primary:not(:disabled), .main .btn-primary[data-save]:not(:disabled)')
    if (btn) btn.click()
    else window.dispatchEvent(new Event('autosave-flush')) // auto-saving pages save now
    return
  }
  const n = Number(e.key)
  if (n >= 1 && n <= 9 && !e.shiftKey) {
    const path = flatPaths.value[n - 1]
    if (path) { e.preventDefault(); router.push(path) }
  }
}
onUnmounted(() => window.removeEventListener('keydown', onKey))

// Drop a share card (.png), .svrc or .json anywhere to import it.
const dragging = ref(false)
let dragDepth = 0
const isFileDrag = (e: DragEvent) => !!e.dataTransfer && Array.from(e.dataTransfer.types).includes('Files')
function onDragEnter(e: DragEvent) { if (isFileDrag(e)) { dragDepth++; dragging.value = true } }
function onDragLeave(e: DragEvent) { if (isFileDrag(e) && --dragDepth <= 0) { dragDepth = 0; dragging.value = false } }
function onDragOver(e: DragEvent) { if (isFileDrag(e)) e.preventDefault() }
function onDrop(e: DragEvent) {
  if (!isFileDrag(e)) return
  e.preventDefault()
  dragDepth = 0; dragging.value = false
  const f = e.dataTransfer!.files[0]
  if (f) previewShare(f)
}
// Pasting a share code (Ctrl+V outside inputs) opens the import preview.
function onPaste(e: ClipboardEvent) {
  const el = e.target as HTMLElement
  if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return
  const text = e.clipboardData?.getData('text') || ''
  if (text.trim().startsWith('SVRC1:')) { e.preventDefault(); previewShare(text) }
}
onUnmounted(() => {
  window.removeEventListener('dragenter', onDragEnter)
  window.removeEventListener('dragleave', onDragLeave)
  window.removeEventListener('dragover', onDragOver)
  window.removeEventListener('drop', onDrop)
  window.removeEventListener('paste', onPaste)
})

onMounted(() => {
  window.addEventListener('dragenter', onDragEnter)
  window.addEventListener('dragleave', onDragLeave)
  window.addEventListener('dragover', onDragOver)
  window.addEventListener('drop', onDrop)
  window.addEventListener('paste', onPaste)
  window.addEventListener('keydown', onKey)
  startStatusPolling()
  // First run (no tour_done in localStorage) auto-starts the onboarding tour.
  start()
})
</script>

<template>
  <div class="shell" :class="{ collapsed: !sidebarOpen }">
    <!-- Sidebar -->
    <aside class="sidebar" :class="{ closed: !sidebarOpen }">
      <div class="sidebar-brand">
        <span class="brand-mark"><Zap :size="16" :stroke-width="2.5" /></span>
        <span class="brand-name">Shocking VRChat</span>
      </div>
      <nav class="nav">
        <template v-for="item in navItems" :key="item.key || item.path">
          <!-- Group with children -->
          <template v-if="item.children">
            <button
              class="nav-link nav-group-toggle"
              :class="{ expanded: expandedGroups.has(item.key!) }"
              :title="sidebarOpen ? undefined : item.label"
              :aria-expanded="expandedGroups.has(item.key!)"
              @click="toggleGroup(item.key!)"
            >
              <component :is="item.icon" class="nav-ico" :size="18" :stroke-width="1.75" />
              <span class="nav-txt">{{ item.label }}</span>
              <ChevronDown class="nav-arrow" :size="14" />
            </button>
            <div class="nav-children" v-show="expandedGroups.has(item.key!)">
              <router-link
                v-for="child in item.children" :key="child.path"
                :to="child.path!"
                class="nav-link nav-child"
                active-class="active"
                :title="sidebarOpen ? undefined : child.label"
              >
                <component :is="child.icon" class="nav-ico" :size="16" :stroke-width="1.75" />
                <span class="nav-txt">{{ child.label }}</span>
              </router-link>
            </div>
          </template>
          <!-- Normal link -->
          <router-link
            v-else
            :to="item.path!"
            class="nav-link"
            active-class="active"
            :title="sidebarOpen ? undefined : item.label"
            :data-tour="item.path ? TOUR_ANCHORS[item.path] : undefined"
          >
            <component :is="item.icon" class="nav-ico" :size="18" :stroke-width="1.75" />
            <span class="nav-txt">{{ item.label }}</span>
          </router-link>
        </template>
      </nav>

      <router-link
        to="/dashboard"
        class="conn-status"
        :class="'conn-' + statusKind"
        :title="statusText"
        role="status"
        aria-live="polite"
      >
        <span class="conn-dot" />
        <span class="conn-txt">{{ statusText }}</span>
      </router-link>

      <div class="sidebar-footer">
        <button
          class="icon-btn"
          :title="locale === 'zh' ? 'Switch to English' : '切换到中文'"
          @click="toggleLocale"
        >
          <Languages :size="16" />
          <span class="icon-btn-label">{{ locale === 'zh' ? 'EN' : '中' }}</span>
        </button>
        <button
          class="icon-btn"
          :title="effectiveTheme === 'dark' ? t('nav.lightMode') : t('nav.darkMode')"
          :aria-label="effectiveTheme === 'dark' ? t('nav.lightMode') : t('nav.darkMode')"
          @click="toggleTheme"
        >
          <Sun v-if="effectiveTheme === 'dark'" :size="16" />
          <Moon v-else :size="16" />
        </button>
        <button class="icon-btn icon-btn-danger" :title="t('nav.shutdown')" @click="shutdownApp">
          <Power :size="16" />
        </button>
        <button
          v-if="!narrow"
          class="icon-btn"
          :title="sidebarOpen ? t('nav.collapse') : t('nav.expand')"
          :aria-label="sidebarOpen ? t('nav.collapse') : t('nav.expand')"
          @click="sidebarPref = !sidebarPref"
        >
          <PanelLeftClose v-if="sidebarOpen" :size="16" />
          <PanelLeftOpen v-else :size="16" />
        </button>
      </div>
    </aside>

    <!-- Main -->
    <main class="main">
      <router-view v-slot="{ Component: View }">
        <Transition name="page" mode="out-in">
          <keep-alive>
            <component :is="View" />
          </keep-alive>
        </Transition>
      </router-view>
    </main>

    <!-- Guided tour coachmark overlay -->
    <TourOverlay />
    <ShareExportDialog />
    <ShareImportDialog />
    <div v-if="dragging" class="drop-overlay"><div>{{ t('share.dropOverlay') }}</div></div>
    <AppDialog />
    <TopProgress />
  </div>
</template>

<style scoped>
.drop-overlay { position: fixed; inset: 0; z-index: 950; display: grid; place-items: center; background: var(--overlay); pointer-events: none; }
.drop-overlay div { padding: 28px 40px; border: 2px dashed var(--accent); border-radius: var(--radius-xl); background: var(--bg-popover); font-size: var(--text-lg); color: var(--text); }
.shell {
  display: grid;
  grid-template-columns: 208px 1fr;
  min-height: 100vh;
}
.shell.collapsed { grid-template-columns: 60px 1fr; }

/* Sidebar */
.sidebar {
  background: var(--bg-sidebar);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  padding: var(--sp-4) 0 var(--sp-3);
  position: sticky;
  top: 0;
  height: 100vh;
  overflow: hidden;
}
.sidebar-brand {
  display: flex;
  align-items: center;
  gap: var(--sp-3);
  padding: var(--sp-1) var(--sp-4) var(--sp-5);
  min-height: 44px;
}
.brand-mark {
  width: 28px; height: 28px; flex-shrink: 0;
  display: grid; place-items: center;
  border-radius: var(--radius-sm);
  background: var(--gradient-brand);
  color: var(--text-on-accent);
}
.brand-name {
  font-size: var(--text-base);
  font-weight: 700;
  letter-spacing: -0.01em;
  white-space: nowrap;
  transition: opacity 140ms ease;
}

.nav {
  flex: 1;
  padding: 0 var(--sp-2);
  display: flex; flex-direction: column; gap: 2px;
  overflow-y: auto; overflow-x: hidden;
}
.nav-link {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  gap: var(--sp-3);
  height: 36px;
  padding: 0 var(--sp-3);
  border-radius: var(--radius-sm);
  color: var(--text-muted);
  text-decoration: none;
  font-size: var(--text-sm);
  font-weight: 500;
  transition: var(--transition-props);
  position: relative;
}
.nav-link:hover { color: var(--text); background: var(--bg-hover); text-decoration: none; }
.nav-link.active { color: var(--text); background: var(--bg-active); }
/* active indicator bar */
.nav-link.active::before {
  content: '';
  position: absolute; left: 0; top: 9px; bottom: 9px; width: 3px;
  border-radius: 0 3px 3px 0;
  background: var(--accent);
}
.nav-ico { flex-shrink: 0; }
.nav-txt { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; transition: opacity 140ms ease; }
.nav-arrow { margin-left: auto; flex-shrink: 0; transition: transform var(--transition), opacity 140ms ease; transform: rotate(-90deg); }
.nav-group-toggle.expanded .nav-arrow { transform: rotate(0); }
.nav-group-toggle { width: 100%; text-align: left; background: none; border: none; cursor: pointer; font: inherit; }
.nav-children { display: flex; flex-direction: column; gap: 2px; }
.nav-child { height: 32px; padding-left: calc(var(--sp-3) + 12px); }

.sidebar.closed .brand-name,
.sidebar.closed .nav-txt,
.sidebar.closed .nav-arrow { opacity: 0; pointer-events: none; }
.sidebar.closed .nav-child { padding-left: var(--sp-3); }
.sidebar.closed .sidebar-brand { padding-left: 16px; }

/* Global connection status */
.conn-status {
  display: flex; align-items: center; gap: var(--sp-2);
  margin: var(--sp-2) var(--sp-2) 0;
  padding: var(--sp-2) var(--sp-3);
  border-radius: var(--radius-sm);
  font-size: var(--text-xs);
  color: var(--text-muted);
  text-decoration: none;
  transition: var(--transition-props);
}
.conn-status:hover { background: var(--bg-hover); color: var(--text); text-decoration: none; }
.conn-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: var(--text-muted); margin-left: 5px; }
.conn-ok .conn-dot { background: var(--success); box-shadow: 0 0 0 3px var(--success-surface); }
.conn-ok { color: var(--success); }
.conn-err .conn-dot { background: var(--danger); animation: conn-blink 1.2s ease-in-out infinite; }
.conn-err { color: var(--danger); }
@keyframes conn-blink { 50% { opacity: 0.35; } }
.conn-txt { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; transition: opacity 140ms ease; }
.sidebar.closed .conn-txt { opacity: 0; }

/* Footer: compact icon buttons */
.sidebar-footer {
  display: flex;
  gap: var(--sp-1);
  padding: var(--sp-3) var(--sp-3) 0;
  border-top: 1px solid var(--border-subtle);
  margin-top: var(--sp-2);
}
.sidebar.closed .sidebar-footer { flex-direction: column; align-items: center; padding: var(--sp-3) 0 0; }
.icon-btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 4px;
  height: 32px; min-width: 32px; padding: 0 var(--sp-2);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font: inherit; font-size: var(--text-xs); font-weight: 600;
  transition: var(--transition-props);
}
.icon-btn:last-child { margin-left: auto; }
.sidebar.closed .icon-btn:last-child { margin-left: 0; }
.sidebar.closed .icon-btn-label { display: none; }
.icon-btn:hover { color: var(--text); background: var(--bg-hover); }
.icon-btn-danger:hover { color: var(--danger); background: var(--danger-surface); }
.icon-btn:focus-visible, .nav-link:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }

/* Main content */
.main {
  padding: var(--sp-8);
  max-width: 1100px;
  width: 100%;
  margin: 0 auto;
  min-width: 0;
}

/* Mobile (phone browsers opening the LAN UI) */
@media (max-width: 768px) {
  .main { padding: var(--sp-4); }
}
</style>
