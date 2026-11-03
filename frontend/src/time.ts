import { useI18n } from '@/i18n'

/** HH:mm:ss in the UI language, always 24-hour. */
export function formatTime(tsSeconds: number): string {
  const { locale } = useI18n()
  return new Date(tsSeconds * 1000).toLocaleTimeString(locale.value === 'zh' ? 'zh-CN' : 'en-GB', {
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  })
}
