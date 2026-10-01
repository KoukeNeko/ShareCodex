import type { AccountOverview } from './api'

// A window's use, or -1 when it is unknown: no reading, or one that reset.
function used(a: AccountOverview, key: string, now: Date): number {
  const b = (a.buckets ?? []).find((b) => b.key === key)
  if (!b || b.reset || (b.resets_at && new Date(b.resets_at) <= now)) return -1
  return b.used_percent
}

/**
 * Accounts in the order settings.Settings.AccountSort names. Ties, and accounts a custom order does not
 * list yet, keep the server's order.
 */
export function sortAccounts(accounts: AccountOverview[], sort: string, order: string[], now: Date): AccountOverview[] {
  const rank = (a: AccountOverview): number => {
    if (sort === 'five_hour' || sort === 'weekly') return -used(a, sort, now)
    if (sort === 'custom') {
      const i = order.indexOf(a.id)
      return i < 0 ? order.length : i
    }
    return 0
  }
  return accounts
    .map((a, i) => ({ a, i, rank: rank(a) }))
    .sort((x, y) => x.rank - y.rank || x.i - y.i)
    .map((x) => x.a)
}
