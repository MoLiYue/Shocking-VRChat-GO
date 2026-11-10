// Parameter-setup sharing (see internal/app/share.go): global dialog state,
// the share card renderer and download helpers.
import { reactive } from 'vue'

export interface ShareDoc {
  format: string; version: number; app: string; created: string
  title?: string; note?: string; avatar?: string
  channels: Record<'a' | 'b', { params: any[] }>
  mode_config: Record<string, any>
  curves: Record<string, { x: number; y: number }[]>
  overlimit_rules: any[]
  presets: Record<string, unknown>
  reference?: { strength_limit?: Record<string, number> }
}

export const shareState = reactive({
  exportOpen: false,
  importOpen: false,
  /** /api/v1/share/preview response for the import dialog */
  preview: null as any,
  error: '' as string,
})

export function openShareExport() { shareState.exportOpen = true }

/** Upload a dropped / picked file (or a pasted code) for preview. */
export async function previewShare(input: File | string): Promise<void> {
  shareState.error = ''
  shareState.preview = null
  shareState.importOpen = true
  let resp: Response
  try {
    if (typeof input === 'string') {
      resp = await fetch('/api/v1/share/preview', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: input }),
      })
    } else {
      const fd = new FormData()
      fd.append('file', input)
      resp = await fetch('/api/v1/share/preview', { method: 'POST', body: fd })
    }
  } catch { shareState.error = 'network'; return }
  const data = await resp.json().catch(() => ({}))
  if (!resp.ok) { shareState.error = data.code || data.detail || 'invalid'; return }
  shareState.preview = data
}

/** Trigger a browser download for a Blob (works in WebView2 too). */
export function downloadBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

/** File name from Content-Disposition (RFC 5987 filename*). */
export function dispositionName(resp: Response, fallback: string): string {
  const cd = resp.headers.get('Content-Disposition') || ''
  const m = cd.match(/filename\*=UTF-8''([^;]+)/i)
  if (m) { try { return decodeURIComponent(m[1]) } catch {} }
  return fallback
}

// ---------------------------------------------------------------- card

// Fixed palette (the card looks the same whatever the viewer's theme).
const C = {
  bg: '#1e1e2e', panel: '#262637', line: '#3a3a52', text: '#e6e6f0', muted: '#9a9ab0',
  a: '#cba6f7', b: '#89dceb', warn: '#f9e2af', accent: '#89b4fa',
  mode: { distance: '#89b4fa', shock: '#f38ba8', touch: '#a6e3a1', combo: '#f9e2af', boost: '#cba6f7', ranges: '#fab387' } as Record<string, string>,
}

type T = (k: string, p?: Record<string, string | number>) => string

function rr(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath()
  ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r)
  ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath()
}

function fit(ctx: CanvasRenderingContext2D, s: string, max: number): string {
  if (ctx.measureText(s).width <= max) return s
  while (s.length > 1 && ctx.measureText(s + '…').width > max) s = s.slice(0, -1)
  return s + '…'
}

/** Renders the share card (PNG blob) for a share document. */
export async function renderShareCard(doc: ShareDoc, t: T, modeLabel: (m: string) => string): Promise<Blob> {
  const W = 960, pad = 32, font = '"Segoe UI", "Microsoft YaHei", "PingFang SC", sans-serif'
  const rows: { ch: 'a' | 'b'; p: any }[] = []
  for (const ch of ['a', 'b'] as const) for (const p of doc.channels?.[ch]?.params || []) rows.push({ ch, p })
  const rules = doc.overlimit_rules || []
  const rowH = 34
  const H = 150 + Math.max(1, rows.length) * rowH + (rules.length ? 56 + rules.length * 28 : 0) + 70
  const dpr = 2
  const cv = document.createElement('canvas')
  cv.width = W * dpr; cv.height = H * dpr
  const ctx = cv.getContext('2d')!
  ctx.scale(dpr, dpr)
  ctx.fillStyle = C.bg; ctx.fillRect(0, 0, W, H)
  const grad = ctx.createLinearGradient(0, 0, W, 0)
  grad.addColorStop(0, C.a); grad.addColorStop(1, C.b)
  ctx.fillStyle = grad; ctx.fillRect(0, 0, W, 6)

  // header
  ctx.fillStyle = C.text; ctx.font = `700 28px ${font}`
  ctx.fillText(fit(ctx, doc.title || t('share.cardDefaultTitle'), W - 2 * pad), pad, 56)
  ctx.font = `14px ${font}`; ctx.fillStyle = C.muted
  const sub = [doc.avatar ? `${t('share.cardAvatar')}: ${doc.avatar}` : '', new Date(doc.created).toLocaleDateString(), `Shocking VRChat ${doc.app}`].filter(Boolean).join('  ·  ')
  ctx.fillText(sub, pad, 82)
  if (doc.note) { ctx.fillStyle = C.text; ctx.font = `15px ${font}`; ctx.fillText(fit(ctx, doc.note, W - 2 * pad), pad, 110) }

  // params
  let y = 140
  ctx.font = `600 13px ${font}`; ctx.fillStyle = C.muted
  ctx.fillText(t('share.cardParams'), pad, y - 6)
  for (const { ch, p } of rows) {
    rr(ctx, pad, y, W - 2 * pad, rowH - 6, 8); ctx.fillStyle = C.panel; ctx.fill()
    // channel tag
    rr(ctx, pad + 10, y + 5, 22, 18, 5); ctx.fillStyle = ch === 'a' ? C.a : C.b; ctx.fill()
    ctx.fillStyle = C.bg; ctx.font = `700 12px ${font}`; ctx.fillText(ch.toUpperCase(), pad + 16, y + 18)
    ctx.fillStyle = C.text; ctx.font = `14px Consolas, "Cascadia Mono", monospace`
    ctx.fillText(fit(ctx, String(p.path), 400), pad + 44, y + 19)
    const mx = pad + 470
    if (p.mode === 'ranges' && Array.isArray(p.ranges)) {
      const bw = W - pad - 12 - mx
      for (const r of p.ranges) {
        const x0 = mx + r.from * bw, w = Math.max(2, (r.to - r.from) * bw - 2)
        rr(ctx, x0, y + 5, w, 18, 4); ctx.fillStyle = C.mode[r.mode] || C.accent; ctx.fill()
        ctx.fillStyle = C.bg; ctx.font = `600 11px ${font}`
        ctx.fillText(fit(ctx, modeLabel(r.mode), w - 6), x0 + 4, y + 18)
      }
    } else {
      const label = modeLabel(p.mode || 'distance')
      ctx.font = `600 12px ${font}`
      const w = ctx.measureText(label).width + 16
      rr(ctx, mx, y + 5, w, 18, 9); ctx.fillStyle = C.mode[p.mode] || C.accent; ctx.fill()
      ctx.fillStyle = C.bg; ctx.fillText(label, mx + 8, y + 18)
      const curve = doc.curves?.[p.path]
      if (curve?.length) {
        // tiny curve thumbnail
        const cx = W - pad - 70, cw = 56, ch2 = 18
        ctx.strokeStyle = C.muted; ctx.lineWidth = 1.5; ctx.beginPath()
        curve.forEach((pt, i) => { const X = cx + pt.x * cw, Y = y + 23 - pt.y * ch2; if (i) ctx.lineTo(X, Y); else ctx.moveTo(X, Y) })
        ctx.stroke()
      }
    }
    y += rowH
  }
  if (!rows.length) { ctx.fillStyle = C.muted; ctx.font = `14px ${font}`; ctx.fillText('—', pad + 10, y + 18); y += rowH }

  // overlimit rules
  if (rules.length) {
    y += 22
    ctx.font = `600 13px ${font}`; ctx.fillStyle = C.warn
    ctx.fillText(t('share.cardRules'), pad, y)
    y += 12
    for (const r of rules) {
      const chs = r.channel === 'both' ? 'A+B' : String(r.channel || '').toUpperCase()
      const boost = r.boost_unit === 'percent' ? `+${r.limit_boost}%` : `+${r.limit_boost}`
      const c = r.condition || {}
      ctx.fillStyle = C.text; ctx.font = `14px ${font}`
      const line = `${r.name || '-'}   ${c.param || ''} ${c.operator || ''} ${c.value ?? ''}   →  ${chs} ${boost}` +
        (r.decay_seconds ? `  ·  ${r.decay_seconds}s` : '')
      ctx.fillText(fit(ctx, line, W - 2 * pad), pad + 10, y + 16)
      y += 28
    }
  }

  // footer
  ctx.fillStyle = C.line; ctx.fillRect(pad, H - 46, W - 2 * pad, 1)
  ctx.fillStyle = C.muted; ctx.font = `13px ${font}`
  ctx.fillText(t('share.cardFooter'), pad, H - 20)
  return new Promise((res, rej) => cv.toBlob((b) => (b ? res(b) : rej(new Error('toBlob'))), 'image/png'))
}
