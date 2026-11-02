/** An estimate from a provider's previous request, never a cache-server query. */
export interface PromptCacheSnapshot {
  anchorAt?: number
  ttlMs?: number
  recacheTokens?: number
  compacted?: boolean
  ambiguousHistory?: boolean
}
