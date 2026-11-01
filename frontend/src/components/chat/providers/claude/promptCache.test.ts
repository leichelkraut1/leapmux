import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { NATIVE_CONVERSATION_FORMAT } from '~/generated/contracts/native-conversations'
import { AgentChatMessageSchema, ContentCompression } from '~/generated/proto/leapmux/v1/agent_pb'
import { projectClaudeNativeConversation } from './nativeConversation'
import { claudePromptCacheFromMessages } from './promptCache'

const START = Date.parse('2026-11-01T12:00:00Z')
function row(seq: number, envelope: Record<string, unknown>, at = START, session = 'generated-session') {
  return create(AgentChatMessageSchema, {
    id: `generated-${seq}`,
    seq: BigInt(seq),
    contentCompression: ContentCompression.NONE,
    createdAt: new Date(at).toISOString(),
    agentSessionId: session,
    content: new TextEncoder().encode(JSON.stringify(envelope)),
  })
}
function assistant(id: string, ttl?: '5m' | '1h', extras: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      id,
      model: 'generated-model',
      content: [{ type: 'text', text: 'Generated harmless response' }],
      usage: {
        input_tokens: 100,
        cache_read_input_tokens: 200,
        cache_creation_input_tokens: 300,
        ...(ttl ? { cache_creation: { [ttl === '1h' ? 'ephemeral_1h_input_tokens' : 'ephemeral_5m_input_tokens']: 300 } } : {}),
      },
    },
    ...extras,
  }
}

describe('claudePromptCacheFromMessages', () => {
  it.each([['5m', 300_000], ['1h', 3_600_000]] as const)('reads the observed %s bucket and sums input without output tokens', (ttl, ttlMs) => {
    expect(claudePromptCacheFromMessages([row(1, assistant('a', ttl))])).toEqual({ anchorAt: START, ttlMs, recacheTokens: 600 })
  })

  it('keeps an unknown lifetime unknown instead of assuming a subscription tier', () => {
    expect(claudePromptCacheFromMessages([row(1, assistant('a'))])).toEqual({ anchorAt: START, recacheTokens: 600 })
  })

  it('uses the earlier request time so long generation does not renew the cache', () => {
    const request = row(1, { type: 'user', timestamp: new Date(START).toISOString() })
    expect(claudePromptCacheFromMessages([request, row(2, assistant('a', '5m'), START + 120_000)])?.anchorAt).toBe(START)
  })

  it('does not use a later or invalid request timestamp ahead of the response', () => {
    const request = row(1, { type: 'user', timestamp: new Date(START + 10_000).toISOString() })
    expect(claudePromptCacheFromMessages([request, row(2, assistant('a', '5m'))])?.anchorAt).toBe(START)
    const message = row(2, assistant('a', '5m', { timestamp: 'invalid' }))
    message.createdAt = ''
    expect(claudePromptCacheFromMessages([message])).toEqual({ ttlMs: 300_000, recacheTokens: 600 })
  })

  it('preserves the original anchor for split blocks and repeated assistant frames', () => {
    const result = claudePromptCacheFromMessages([
      row(1, assistant('same', '1h')),
      row(2, assistant('same'), START + 90_000),
    ])
    expect(result).toEqual({ anchorAt: START, ttlMs: 3_600_000, recacheTokens: 600 })
  })

  it('reads usage once from actual native block projection without changing its timestamp', () => {
    const session = '11111111-2222-3333-4444-555555555555'
    const message = assistant('projected-native', '1h')
    const native = { ...message, sessionId: session, timestamp: new Date(START).toISOString(), message: {
      ...message.message,
      content: [{ type: 'thinking', thinking: 'Generated thinking' }, { type: 'text', text: 'Generated visible text' }],
    } }
    const projected = projectClaudeNativeConversation([
      { id: 'generated-native-record', seq: 1n, nativeJson: new TextEncoder().encode(JSON.stringify(native)) },
    ], NATIVE_CONVERSATION_FORMAT.ClaudeJSONL, session)
    expect(projected).toHaveLength(2)
    expect(claudePromptCacheFromMessages(projected, { savedHistory: true })).toEqual({ anchorAt: START, ttlMs: 3_600_000, recacheTokens: 600 })
  })

  it('carries an observed lifetime through later cache reads without a creation bucket', () => {
    expect(claudePromptCacheFromMessages([
      row(1, assistant('a', '5m')),
      row(2, assistant('b'), START + 100_000),
    ])).toEqual({ anchorAt: START + 100_000, ttlMs: 300_000, recacheTokens: 600 })
  })

  it('processes loaded older pages in sequence order without replacing newer usage', () => {
    expect(claudePromptCacheFromMessages([
      row(8, assistant('latest', '1h'), START + 100_000),
      row(1, assistant('old', '5m')),
    ])).toEqual({ anchorAt: START + 100_000, ttlMs: 3_600_000, recacheTokens: 600 })
  })

  it('ignores subagents, synthetic messages, api errors and cumulative results', () => {
    const base = row(1, assistant('a', '5m'))
    for (const extra of [{ parent_tool_use_id: 'child' }, { isSidechain: true }, { isSynthetic: true }, { is_api_error_message: true }, { error: 'server_error' }]) {
      expect(claudePromptCacheFromMessages([base, row(2, assistant('ignored', '1h', extra), START + 10_000)])).toEqual({ anchorAt: START, ttlMs: 300_000, recacheTokens: 600 })
    }
    expect(claudePromptCacheFromMessages([base, row(3, { type: 'result', usage: { input_tokens: 999999 } })])?.recacheTokens).toBe(600)
  })

  it('clears the numeric estimate at compaction and waits for new model usage', () => {
    const boundary = row(2, { type: 'system', subtype: 'compact_boundary' }, START + 1000)
    expect(claudePromptCacheFromMessages([row(1, assistant('a', '5m')), boundary])).toEqual({ compacted: true })
    expect(claudePromptCacheFromMessages([row(1, assistant('a', '5m')), boundary, row(3, assistant('a'), START + 2000)])).toEqual({ compacted: true })
    expect(claudePromptCacheFromMessages([boundary, row(3, assistant('b', '1h'))])?.compacted).toBeUndefined()
  })

  it('resets observed lifetime when the native session changes', () => {
    expect(claudePromptCacheFromMessages([
      row(1, assistant('a', '1h'), START, 'session-a'),
      row(2, assistant('b'), START + 1000, 'session-b'),
    ])?.ttlMs).toBeUndefined()
  })

  it('marks native forks ambiguous while allowing split blocks of the same model message', () => {
    const first = row(1, assistant('a', '1h', { uuid: 'a1', parentUuid: 'parent' }))
    const split = row(2, assistant('a', '1h', { uuid: 'a2', parentUuid: 'parent' }))
    expect(claudePromptCacheFromMessages([first, split], { savedHistory: true })?.ambiguousHistory).toBeUndefined()
    const fork = row(3, assistant('b', '1h', { uuid: 'b1', parentUuid: 'parent' }))
    expect(claudePromptCacheFromMessages([first, fork], { savedHistory: true })).toEqual({ ambiguousHistory: true })
  })

  it('does not mistake progress, synthetic rows or parallel tool results for a native fork', () => {
    const conversation = [
      row(1, assistant('a', '1h', { uuid: 'a1', parentUuid: 'prompt' })),
      row(2, { type: 'progress', uuid: 'progress-a', parentUuid: 'a1' }),
      row(3, { type: 'progress', uuid: 'progress-b', parentUuid: 'a1' }),
      row(4, assistant('synthetic', '5m', { uuid: 'synthetic', parentUuid: 'prompt', isSynthetic: true })),
      row(5, { type: 'user', uuid: 'result-a', parentUuid: 'a1', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'tool-a', content: 'Generated tool output A' }] } }),
      row(6, { type: 'user', uuid: 'result-b', parentUuid: 'a1', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'tool-b', content: 'Generated tool output B' }] } }),
    ]
    expect(claudePromptCacheFromMessages(conversation, { savedHistory: true })).toEqual({ anchorAt: START, ttlMs: 3_600_000, recacheTokens: 600 })
  })

  it('rejects invalid and overflowing token counts', () => {
    for (const input of [-1, '100', Number.MAX_SAFE_INTEGER]) {
      const payload = assistant('a', '5m')
      expect(claudePromptCacheFromMessages([row(1, { ...payload, message: { ...payload.message, usage: { ...payload.message.usage, input_tokens: input } } })])).toBeUndefined()
    }
  })
})
