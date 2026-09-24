export * as Desktop from '../../bindings/github.com/KoukeNeko/ShareCodex/internal/client/desktop/service'
export type { State, ProviderState, LocalAccount, Invite } from '../../bindings/github.com/KoukeNeko/ShareCodex/internal/client/agent/models'
export type { AccountOverview, BucketOverview, MemberShare } from '../../bindings/github.com/KoukeNeko/ShareCodex/internal/syncapi/models'

/** Go errors reach the frontend as rejected promises carrying a message. */
export function errorMessage(err: unknown): string {
  if (err && typeof err === 'object' && 'message' in err) return String((err as { message: unknown }).message)
  return String(err)
}
