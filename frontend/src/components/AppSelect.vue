<script setup lang="ts">
// Themed replacement for <select>. The native popup list cannot be styled
// (it is drawn by the OS), so this renders its own listbox in a popover.
//
// Keyboard: Enter/Space/ArrowDown open, arrows move, Home/End jump,
// Enter selects, Esc/Tab close, typing jumps to / filters matching items.
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Check, ChevronDown, Search } from 'lucide-vue-next'

export interface SelectOption { value: any; label: string; hint?: string }

const props = withDefaults(defineProps<{
  modelValue: any
  options: (SelectOption | string | number)[]
  placeholder?: string
  /** Show a filter box; default: automatically when there are > 10 options. */
  searchable?: boolean | null
  disabled?: boolean
  mono?: boolean
  /** Compact trigger matching .btn-sm (toolbars, card headers). */
  small?: boolean
}>(), { placeholder: '', searchable: null, disabled: false, mono: false, small: false })

const emit = defineEmits<{ (e: 'update:modelValue', v: any): void; (e: 'change', v: any): void }>()

const opts = computed<SelectOption[]>(() =>
  props.options.map((o) => (typeof o === 'object' ? o : { value: o, label: String(o) })))
const selected = computed(() => opts.value.find((o) => o.value === props.modelValue))
const canSearch = computed(() => props.searchable ?? opts.value.length > 10)

const open = ref(false)
const query = ref('')
const active = ref(0)
const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const list = ref<HTMLElement | null>(null)
const searchEl = ref<HTMLInputElement | null>(null)
const pos = ref({ top: 0, left: 0, width: 0, maxH: 280, up: false })
const uid = 'sel-' + Math.random().toString(36).slice(2, 8)

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? opts.value.filter((o) => o.label.toLowerCase().includes(q)) : opts.value
})

function place() {
  const el = trigger.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const below = window.innerHeight - r.bottom - 12
  const above = r.top - 12
  const up = below < 220 && above > below
  const maxH = Math.max(140, Math.min(320, up ? above : below))
  pos.value = { top: up ? r.top - 6 : r.bottom + 6, left: r.left, width: Math.max(r.width, 160), maxH, up }
}

async function openList() {
  if (props.disabled || open.value) return
  query.value = ''
  const i = filtered.value.findIndex((o) => o.value === props.modelValue)
  active.value = i >= 0 ? i : 0
  place()
  open.value = true
  await nextTick()
  if (canSearch.value) searchEl.value?.focus()
  scrollActive()
}

function close(focusTrigger = true) {
  if (!open.value) return
  open.value = false
  if (focusTrigger) trigger.value?.focus()
}

function choose(o: SelectOption | undefined) {
  if (!o) return
  if (o.value !== props.modelValue) {
    emit('update:modelValue', o.value)
    emit('change', o.value)
  }
  close()
}

function scrollActive() {
  nextTick(() => list.value?.querySelector<HTMLElement>(`[data-i="${active.value}"]`)?.scrollIntoView({ block: 'nearest' }))
}

function move(d: number) {
  const n = filtered.value.length
  if (!n) return
  active.value = (active.value + d + n) % n
  scrollActive()
}

let typeBuf = ''
let typeTimer = 0
function onKey(e: KeyboardEvent) {
  if (props.disabled) return
  if (!open.value) {
    if (['Enter', ' ', 'ArrowDown', 'ArrowUp'].includes(e.key)) { e.preventDefault(); openList() }
    return
  }
  switch (e.key) {
    case 'ArrowDown': e.preventDefault(); move(1); break
    case 'ArrowUp': e.preventDefault(); move(-1); break
    case 'Home': e.preventDefault(); active.value = 0; scrollActive(); break
    case 'End': e.preventDefault(); active.value = filtered.value.length - 1; scrollActive(); break
    case 'Enter': e.preventDefault(); choose(filtered.value[active.value]); break
    case 'Escape': e.preventDefault(); e.stopPropagation(); close(); break
    case 'Tab': close(false); break
    default:
      // Type-ahead when there is no search box
      if (!canSearch.value && e.key.length === 1) {
        typeBuf += e.key.toLowerCase()
        clearTimeout(typeTimer)
        typeTimer = window.setTimeout(() => { typeBuf = '' }, 600)
        const i = filtered.value.findIndex((o) => o.label.toLowerCase().startsWith(typeBuf))
        if (i >= 0) { active.value = i; scrollActive() }
      }
  }
}

watch(query, () => { active.value = 0 })

function onDocDown(e: Event) {
  const t = e.target as Node
  if (root.value?.contains(t) || list.value?.parentElement?.contains(t)) return
  close(false)
}
function onViewportChange(e: Event) {
  // Scrolling inside the list itself must not close it
  if (e.type === 'scroll' && list.value?.contains(e.target as Node)) return
  close(false)
}
watch(open, (v) => {
  if (v) {
    document.addEventListener('mousedown', onDocDown, true)
    window.addEventListener('resize', onViewportChange)
    window.addEventListener('scroll', onViewportChange, true)
  } else {
    document.removeEventListener('mousedown', onDocDown, true)
    window.removeEventListener('resize', onViewportChange)
    window.removeEventListener('scroll', onViewportChange, true)
  }
})
onBeforeUnmount(() => { open.value = false })
</script>

<template>
  <div ref="root" class="app-select" :class="{ open, disabled, mono, small }">
    <button
      ref="trigger"
      type="button"
      class="sel-trigger"
      role="combobox"
      :aria-expanded="open"
      :aria-controls="uid"
      aria-haspopup="listbox"
      :disabled="disabled"
      @click="open ? close() : openList()"
      @keydown="onKey"
    >
      <span class="sel-value" :class="{ placeholder: !selected }">{{ selected ? selected.label : placeholder }}</span>
      <ChevronDown class="sel-chevron" :size="15" />
    </button>

    <Teleport to="body">
      <Transition name="sel-pop">
        <div
          v-if="open"
          class="sel-pop"
          :class="{ up: pos.up, mono }"
          :style="{ left: pos.left + 'px', top: pos.top + 'px', width: pos.width + 'px' }"
        >
          <div v-if="canSearch" class="sel-search">
            <Search :size="14" />
            <input ref="searchEl" v-model="query" type="text" :placeholder="'…'" @keydown="onKey" />
          </div>
          <ul :id="uid" ref="list" class="sel-list" role="listbox" :style="{ maxHeight: pos.maxH + 'px' }">
            <li
              v-for="(o, i) in filtered" :key="String(o.value)"
              :data-i="i"
              role="option"
              :aria-selected="o.value === modelValue"
              class="sel-opt"
              :class="{ active: i === active, selected: o.value === modelValue }"
              @mousemove="active = i"
              @mousedown.prevent
              @click="choose(o)"
            >
              <span class="sel-opt-label">{{ o.label }}</span>
              <span v-if="o.hint" class="sel-opt-hint">{{ o.hint }}</span>
              <Check v-if="o.value === modelValue" class="sel-check" :size="14" />
            </li>
            <li v-if="!filtered.length" class="sel-empty">—</li>
          </ul>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style>
/* Not scoped: the popover is teleported to <body>. */
.app-select { position: relative; display: inline-flex; min-width: 120px; vertical-align: middle; }
.field .app-select, .setting .app-select { display: flex; width: 100%; }
.sel-trigger {
  display: flex; align-items: center; gap: var(--sp-2); width: 100%;
  min-height: 38px; padding: 0 var(--sp-3) 0 var(--sp-4);
  border: 1px solid var(--border); border-radius: var(--radius-md);
  background: var(--bg-input); color: var(--text);
  font-size: var(--text-sm); text-align: left; cursor: pointer;
  transition: var(--transition-props);
}
.sel-trigger:hover { border-color: var(--border-hover); }
.app-select.open .sel-trigger, .sel-trigger:focus-visible {
  outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px rgba(var(--accent-rgb), 0.18);
}
.app-select.disabled .sel-trigger { opacity: 0.5; cursor: not-allowed; }
.sel-value { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sel-value.placeholder { color: var(--text-muted); }
.app-select.mono .sel-value, .sel-pop.mono .sel-opt-label { font-family: var(--font-mono); font-size: var(--text-xs); }
.app-select.small .sel-trigger, .filter-select .sel-trigger, .edit-select .sel-trigger, .cond-op .sel-trigger { min-height: 30px; padding-left: var(--sp-3); font-size: var(--text-xs); border-radius: var(--radius-sm); }
.sel-chevron { flex-shrink: 0; color: var(--text-muted); transition: transform 160ms ease; }
.app-select.open .sel-chevron { transform: rotate(180deg); color: var(--accent); }

.sel-pop {
  position: fixed; z-index: 1100;
  background: var(--bg-popover);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-pop);
  overflow: hidden;
}
.sel-pop.up { transform: translateY(-100%); }
.sel-search {
  display: flex; align-items: center; gap: var(--sp-2);
  padding: var(--sp-2) var(--sp-3);
  border-bottom: 1px solid var(--border-subtle);
  color: var(--text-muted);
}
.sel-search input {
  flex: 1; min-width: 0; padding: 4px 0 !important;
  border: none !important; background: transparent !important; box-shadow: none !important;
  font-size: var(--text-sm); color: var(--text);
}
.sel-list { list-style: none; margin: 0; padding: 4px; overflow-y: auto; overscroll-behavior: contain; }
.sel-opt {
  display: flex; align-items: center; gap: var(--sp-2);
  padding: 7px var(--sp-3); border-radius: var(--radius-sm);
  font-size: var(--text-sm); color: var(--text-label);
  cursor: pointer; user-select: none;
}
.sel-opt.active { background: var(--bg-hover); color: var(--text); }
.sel-opt.selected { color: var(--accent); font-weight: 600; }
.sel-opt-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sel-opt-hint { font-size: var(--text-xs); color: var(--text-muted); }
.sel-check { flex-shrink: 0; }
.sel-empty { padding: var(--sp-3); text-align: center; color: var(--text-muted); font-size: var(--text-sm); }

.sel-pop-enter-active, .sel-pop-leave-active { transition: opacity 120ms ease, margin-top 140ms cubic-bezier(0.2, 0.8, 0.2, 1); }
.sel-pop-enter-from, .sel-pop-leave-to { opacity: 0; margin-top: -4px; }
.sel-pop.up.sel-pop-enter-from, .sel-pop.up.sel-pop-leave-to { margin-top: 4px; }
</style>
