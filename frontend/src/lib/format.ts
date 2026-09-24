const bucketNames: Record<string, string> = {
  five_hour: '5 小時',
  weekly: '每週',
}

export function bucketName(key: string, windowMinutes: number): string {
  if (bucketNames[key]) return bucketNames[key]
  if (windowMinutes >= 1440 && windowMinutes % 1440 === 0) return `${windowMinutes / 1440} 天`
  if (windowMinutes >= 60 && windowMinutes % 60 === 0) return `${windowMinutes / 60} 小時`
  return `${windowMinutes} 分鐘`
}

export function providerName(provider: string): string {
  return provider === 'anthropic' ? 'Claude' : provider === 'openai' ? 'Codex' : provider
}

export function percent(value: number): string {
  if (value > 0 && value < 1) return '<1%'
  return `${Math.round(value)}%`
}

/** "2 小時 15 分後重置" style countdown; returns "" without a reset time. */
export function resetsIn(resetsAt: string | null | undefined, now: Date): string {
  if (!resetsAt) return ''
  const ms = new Date(resetsAt).getTime() - now.getTime()
  if (ms <= 0) return '已重置'
  const minutes = Math.ceil(ms / 60000)
  const days = Math.floor(minutes / 1440)
  const hours = Math.floor((minutes % 1440) / 60)
  const mins = minutes % 60
  if (days > 0) return `${days} 天 ${hours} 小時後重置`
  if (hours > 0) return `${hours} 小時 ${mins} 分後重置`
  return `${mins} 分後重置`
}

/** Compact token count: 950, 12.3K, 4.5M. */
export function tokens(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`
  return String(n)
}

export function clock(iso: string | null | undefined): string {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString('zh-TW', { hour: '2-digit', minute: '2-digit', hour12: false })
}
