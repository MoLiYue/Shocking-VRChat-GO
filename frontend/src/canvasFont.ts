/** Canvas text in the UI font (canvas cannot use CSS variables directly). */
export function canvasFont(px: number, weight = 400): string {
  const fam = getComputedStyle(document.documentElement).getPropertyValue('--font').trim() || 'sans-serif'
  return `${weight} ${px}px ${fam}`
}

/** Read a theme colour (CSS custom property) for canvas drawing. */
export function themeColor(name: string, fallback = '#888'): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback
}
