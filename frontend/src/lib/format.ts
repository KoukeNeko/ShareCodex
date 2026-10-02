import type { UsageTotals } from './api'
import { locale, t } from './i18n.svelte'

/** A bucket's tab label: a limit on one model family is just the family. */
export function bucketTab(key: string, windowMinutes: number): string {
  const family = modelFamily(key)
  if (family) return family
  return bucketName(key, windowMinutes)
}

/** The model family a bucket limits, capitalized, or "" for all models. */
function modelFamily(key: string): string {
  const prefix = ['weekly_', 'five_hour_'].find((p) => key.startsWith(p))
  if (!prefix) return ''
  const family = key.slice(prefix.length)
  return family.charAt(0).toUpperCase() + family.slice(1)
}

export function bucketName(key: string, windowMinutes: number): string {
  if (key === 'five_hour') return t('fiveHour')
  if (key === 'weekly') return t('weekly')
  // A limit on one model family, such as weekly_fable or five_hour_gemini.
  const family = modelFamily(key)
  if (family) return `${key.startsWith('five_hour_') ? t('fiveHour') : t('weekly')} · ${family}`
  if (windowMinutes >= 1440 && windowMinutes % 1440 === 0) return t('days', { count: windowMinutes / 1440 })
  if (windowMinutes >= 60 && windowMinutes % 60 === 0) return t('hours', { count: windowMinutes / 60 })
  return t('minutes', { count: windowMinutes })
}

export function providerName(provider: string): string {
  const names: Record<string, string> = { anthropic: 'Claude', openai: 'Codex', google: 'Antigravity' }
  return names[provider] ?? provider
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

// Tags for the gateways the client recognises (usage.Gateway*); an
// unrecognised gateway gets none.
const gatewayTags: Record<string, { tag: string; name: string }> = {
  opencodex: { tag: 'OCX', name: 'OpenCodex' },
  ollama: { tag: 'Ollama', name: 'Ollama' },
}

/**
 * A model ID made readable, tagged with the gateway it went through. OpenCodex
 * names the models it routes to Claude clients `ocx-claude-<service>--<model>`
 * (older builds `claude-ocx-<service>--<model>`); they show as the model, and
 * the full ID stays in the title.
 */
export function modelName(id: string, gateway = ''): { name: string; via?: string; title: string } {
  // In Codex, OpenCodex lists the models it routes as <service>/<model>.
  const ocx =
    /^(?:ocx-claude|claude-ocx)-(.+?)--(.+)$/.exec(id) ?? (gateway === 'opencodex' ? /^([^/]+)\/(.+)$/.exec(id) : null)
  const g = gatewayTags[ocx ? 'opencodex' : gateway]
  if (!g) return { name: id, title: id }
  return { name: ocx ? ocx[2] : id, via: g.tag, title: [g.name, ocx?.[1], id].filter(Boolean).join(' · ') }
}

/** The tokens a request's prompt used: input, cached input and cache writes. */
export function inputTokens(u: UsageTotals): number {
  return u.input + u.cached_input + u.cache_write
}

/** Every token a request used, as the models' totals count them. */
export function totalTokens(u: UsageTotals): number {
  return inputTokens(u) + u.output
}

/** An estimated price in US dollars; cents only below $100. */
export function usd(n: number): string {
  const digits = n < 100 ? 2 : 0
  return `US$${n.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })}`
}

export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString(locale(), { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
}

export function clock(iso: string | null | undefined): string {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit', hour12: false })
}
