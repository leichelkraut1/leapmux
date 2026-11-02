import type { Component } from 'solid-js'
import type { AgentChatMessage, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal, onCleanup, Show } from 'solid-js'
import * as styles from './PromptCacheNotice.css'
import { providerFor } from './providers/registry'

export interface PromptCacheNoticeProps {
  provider: AgentProvider
  messages: readonly AgentChatMessage[]
  messageVersion?: number
  savedHistory?: boolean
  current?: boolean
  observedAt?: string
}

function duration(ms: number): string {
  const minutes = Math.max(0, Math.floor(ms / 60_000))
  if (minutes < 60)
    return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  return hours < 24 ? `${hours}h ${minutes % 60}m` : `${Math.floor(hours / 24)}d ${hours % 24}h`
}

function tokenLabel(count: number): string {
  return count >= 1_000_000 ? `${(count / 1_000_000).toFixed(1)}M` : count >= 1000 ? `${Math.round(count / 1000)}k` : String(count)
}

/** A UI clock only: this never calls a provider or sends keep-warm traffic. */
export const PromptCacheNotice: Component<PromptCacheNoticeProps> = (props) => {
  const [now, setNow] = createSignal(Date.now())
  const reader = () => providerFor(props.provider)?.session?.promptCacheFromMessages
  const snapshot = createMemo(() => {
    void props.messageVersion
    return reader()?.(props.messages, { savedHistory: props.savedHistory ?? false })
  })
  createEffect(() => {
    if (!reader() || snapshot() === undefined)
      return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 15_000)
    onCleanup(() => clearInterval(timer))
  })
  const text = createMemo(() => {
    // The clock signal schedules age updates. A fresh poll or message may
    // arrive between ticks, so compare it with the wall clock at this read.
    void now()
    const currentTime = Date.now()
    const prefix = props.savedHistory ? 'Saved-history estimate: ' : ''
    const observedAt = props.observedAt ? Date.parse(props.observedAt) : undefined
    if (props.current === false || (props.savedHistory
      && (observedAt === undefined || !Number.isFinite(observedAt) || observedAt > currentTime || currentTime - observedAt > 30_000))) {
      return `${prefix}prompt cache status unavailable; refresh the latest history.`
    }
    const state = snapshot()
    if (state?.ambiguousHistory)
      return `${prefix}prompt cache status unavailable; this history contains branches.`
    if (state?.compacted)
      return `${prefix}cache estimate unavailable after compaction; the previous token count no longer describes this context.`
    if (state?.anchorAt === undefined || state.ttlMs === undefined || state.anchorAt > currentTime)
      return `${prefix}prompt cache status unavailable; no reliable expiry estimate.`
    const remaining = Math.min(state.ttlMs, state.anchorAt + state.ttlMs - currentTime)
    if (remaining > 0)
      return `${prefix}prompt cache likely warm, about ${Math.ceil(remaining / 60_000)} min left.`
    const count = state.recacheTokens === undefined ? 'the conversation' : `about ${tokenLabel(state.recacheTokens)} input tokens`
    return `${prefix}Context idle ${duration(currentTime - state.anchorAt)}. The prompt cache has likely expired; your next message may re-cache ${count}.`
  })
  return (
    <Show when={snapshot() !== undefined}>
      <p class={styles.notice} role="status" data-testid="prompt-cache-notice">{text()}</p>
    </Show>
  )
}
