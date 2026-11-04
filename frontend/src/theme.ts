// Theme: 'system' | 'dark' | 'light'. The effective theme is written to
// <html data-theme>; index.html applies it before first paint.
import { ref } from 'vue'

export type ThemePref = 'system' | 'dark' | 'light'
const KEY = 'theme'
const mq = window.matchMedia('(prefers-color-scheme: light)')

export const themePref = ref<ThemePref>((localStorage.getItem(KEY) as ThemePref) || 'dark')
export const effectiveTheme = ref<'dark' | 'light'>('dark')

function resolve(p: ThemePref): 'dark' | 'light' {
  return p === 'system' ? (mq.matches ? 'light' : 'dark') : p
}

function apply() {
  const t = resolve(themePref.value)
  const changed = t !== effectiveTheme.value || document.documentElement.dataset.theme !== t
  effectiveTheme.value = t
  document.documentElement.dataset.theme = t
  if (!changed) return
  // Canvases read colours from CSS variables; resize makes them redraw.
  window.dispatchEvent(new Event('resize'))
  window.dispatchEvent(new CustomEvent('themechange', { detail: t }))
  // Native title bar (Windows) follows the theme.
  fetch('/api/v1/ui/theme', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ theme: t }),
  }).catch(() => {})
}

export function setTheme(p: ThemePref) {
  themePref.value = p
  localStorage.setItem(KEY, p)
  apply()
}

mq.addEventListener('change', () => { if (themePref.value === 'system') apply() })
effectiveTheme.value = document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
apply()
