import { locale, t } from './i18n.svelte'

export function bucketName(key: string, windowMinutes: number): string {
  if (key === 'five_hour') return t('fiveHour')
  if (key === 'weekly') return t('weekly')
  if (windowMinutes >= 1440 && windowMinutes % 1440 === 0) return t('days', { count: windowMinutes / 1440 })
  if (windowMinutes >= 60 && windowMinutes % 60 === 0) return t('hours', { count: windowMinutes / 60 })
  return t('minutes', { count: windowMinutes })
}

export function providerName(provider: string): string {
  return provider === 'anthropic' ? 'Claude' : provider === 'openai' ? 'Codex' : provider
}

export function percent(value: number): string {
  if (value > 0 && value < 1) return '<1%'
  return `${Math.round(value)}%`
}

/** Countdown to a window's reset; "" without a reset time. */
export function resetsIn(resetsAt: string | null | undefined, now: Date): string {
  if (!resetsAt) return ''
  const ms = new Date(resetsAt).getTime() - now.getTime()
  if (ms <= 0) return t('reset')
  const total = Math.ceil(ms / 60000)
  const days = Math.floor(total / 1440)
  const hours = Math.floor((total % 1440) / 60)
  const minutes = total % 60
  if (days > 0) return t('resetsInDays', { days, hours })
  if (hours > 0) return t('resetsInHours', { hours, minutes })
  return t('resetsInMinutes', { minutes })
}

/** Compact token count: 950, 12.3K, 4.5M. */
export function tokens(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`
  return String(n)
}

export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString(locale(), { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
}

export function clock(iso: string | null | undefined): string {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
}
