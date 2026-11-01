import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { PromptCacheSnapshot } from '~/models/promptCache'
import { isObject } from '~/lib/jsonPick'
import { getInnerMessage, parseMessageContent } from '~/lib/messageParser'

const CACHE_TTL_5M = 5 * 60_000
const CACHE_TTL_1H = 60 * 60_000

function timestamp(value: unknown): number | undefined {
  if (typeof value !== 'string')
    return undefined
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

function tokens(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined
}

/**
 * Mirrors the public Claude VS Code cache estimate: use the latest main-model
 * usage, observed 5m/1h creation bucket and request time. Result totals are
 * cumulative and cannot estimate the next request. Missing TTL stays unknown.
 */
export function claudePromptCacheFromMessages(
  messages: readonly AgentChatMessage[],
  options: { savedHistory?: boolean } = {},
): PromptCacheSnapshot | undefined {
  let snapshot: PromptCacheSnapshot | undefined
  let requestAt: number | undefined
  let currentMessageId: string | undefined
  let sessionId = ''
  const seenMessages = new Set<string>()
  const nativeChildren = new Map<string, Set<string>>()
  let ambiguousHistory = false

  for (const row of [...messages].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0)) {
    const envelope = getInnerMessage(parseMessageContent(row))
    if (!envelope || envelope.parent_tool_use_id || envelope.isSidechain === true)
      continue
    const nextSession = row.agentSessionId || (typeof envelope.session_id === 'string' ? envelope.session_id : '')
    if (sessionId && nextSession && sessionId !== nextSession) {
      snapshot = undefined
      requestAt = undefined
      currentMessageId = undefined
      seenMessages.clear()
      nativeChildren.clear()
      ambiguousHistory = false
    }
    if (nextSession)
      sessionId = nextSession

    const at = timestamp(envelope.timestamp) ?? timestamp(row.createdAt)
    if ((envelope.type === 'system' && envelope.subtype === 'compact_boundary')
      || (envelope.type === 'user' && envelope.isCompactSummary === true)) {
      snapshot = { compacted: true }
      requestAt = undefined
      continue
    }
    if (envelope.isSynthetic === true || envelope.is_api_error_message === true || envelope.error)
      continue
    const nativeMessage = isObject(envelope.message) ? envelope.message : undefined
    if (nativeMessage?.model === '<synthetic>')
      continue
    // Progress/system records and parallel tool-result siblings are not forks.
    // Assistant block rows sharing one model-message ID are one conversation node.
    const toolResult = Array.isArray(nativeMessage?.content)
      && nativeMessage.content.some(block => isObject(block) && block.type === 'tool_result')
    const canonical = nativeMessage !== undefined && (envelope.type === 'assistant'
      || (envelope.type === 'user' && !toolResult && envelope.is_meta !== true && envelope.isMeta !== true))
    if (options.savedHistory && canonical && typeof envelope.parentUuid === 'string' && envelope.parentUuid
      && typeof envelope.uuid === 'string' && envelope.uuid) {
      const children = nativeChildren.get(envelope.parentUuid) ?? new Set<string>()
      const child = envelope.type === 'assistant' && typeof nativeMessage?.id === 'string'
        ? `assistant:${nativeMessage.id}`
        : envelope.uuid
      children.add(child)
      nativeChildren.set(envelope.parentUuid, children)
      if (children.size > 1)
        ambiguousHistory = true
    }
    if (envelope.type === 'user') {
      if (at !== undefined)
        requestAt = Math.max(requestAt ?? at, at)
      continue
    }
    if (envelope.type !== 'assistant' || !isObject(envelope.message)
      || envelope.message.model === '<synthetic>' || !isObject(envelope.message.usage)) {
      continue
    }
    const usage = envelope.message.usage
    const input = tokens(usage.input_tokens)
    const read = tokens(usage.cache_read_input_tokens ?? 0)
    const write = tokens(usage.cache_creation_input_tokens ?? 0)
    if (input === undefined || read === undefined || write === undefined)
      continue
    const recacheTokens = input + read + write
    if (!Number.isSafeInteger(recacheTokens) || recacheTokens <= 0)
      continue
    const messageId = typeof envelope.message.id === 'string' ? envelope.message.id : row.id
    const repeated = seenMessages.has(messageId)
    if (repeated && (messageId !== currentMessageId || snapshot?.compacted))
      continue
    const anchorAt = repeated ? snapshot?.anchorAt : requestAt !== undefined && (at === undefined || requestAt <= at) ? requestAt : at
    const creation = isObject(usage.cache_creation) ? usage.cache_creation : undefined
    const observedTtl = (tokens(creation?.ephemeral_1h_input_tokens) ?? 0) > 0
      ? CACHE_TTL_1H
      : (tokens(creation?.ephemeral_5m_input_tokens) ?? 0) > 0 ? CACHE_TTL_5M : undefined
    const ttlMs = read + write > 0 ? observedTtl ?? snapshot?.ttlMs : undefined
    snapshot = {
      recacheTokens,
      ...(anchorAt !== undefined ? { anchorAt } : {}),
      ...(ttlMs !== undefined ? { ttlMs } : {}),
    }
    if (!repeated)
      requestAt = undefined
    currentMessageId = messageId
    seenMessages.add(messageId)
  }
  return ambiguousHistory && snapshot !== undefined ? { ambiguousHistory: true } : snapshot
}
