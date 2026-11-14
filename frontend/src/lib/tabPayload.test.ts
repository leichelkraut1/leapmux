import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabPayloadSchema } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { fileTabPayload, imageTabPayload, nativeConversationTabPayload, tabPayloadView, tradingResearchTabPayload } from './tabPayload'

describe('tabPayloadView', () => {
  it('decodes the file branch', () => {
    const payload = create(TabPayloadSchema, fileTabPayload('/repo/a.ts', '/repo'))
    expect(tabPayloadView(payload)).toEqual({ kind: 'file', filePath: '/repo/a.ts', workingDir: '/repo' })
  })

  it('decodes the image branch', () => {
    const payload = create(TabPayloadSchema, imageTabPayload({
      agentId: 'a1',
      seq: 42n,
      imageIndex: 1,
      title: 'Read',
      workingDir: '/repo',
    }))
    expect(tabPayloadView(payload)).toEqual({
      kind: 'image',
      agentId: 'a1',
      seq: 42n,
      imageIndex: 1,
      title: 'Read',
      workingDir: '/repo',
    })
  })

  it('round-trips a native session reference independently of its worktree placement', () => {
    const payload = create(TabPayloadSchema, nativeConversationTabPayload({
      sourceWorkerId: 'source-pc',
      agentProvider: AgentProvider.CODEX,
      sessionId: 'generated-native-session',
      displayName: 'Generated native chat',
      workingDir: '/destination/worktree',
    }))
    expect(tabPayloadView(fromBinary(TabPayloadSchema, toBinary(TabPayloadSchema, payload)))).toEqual({
      kind: 'nativeConversation',
      sourceWorkerId: 'source-pc',
      agentProvider: AgentProvider.CODEX,
      sessionId: 'generated-native-session',
      displayName: 'Generated native chat',
      workingDir: '/destination/worktree',
    })
  })

  it('round-trips the trading research branch, which carries only its kind', () => {
    const payload = create(TabPayloadSchema, tradingResearchTabPayload())
    expect(tabPayloadView(fromBinary(TabPayloadSchema, toBinary(TabPayloadSchema, payload)))).toEqual({
      kind: 'tradingResearch',
      workingDir: '',
    })
  })

  it('returns null for an absent payload', () => {
    expect(tabPayloadView(undefined)).toBeNull()
  })

  it('returns null for a payload stating no kind', () => {
    // A tab kind a newer peer registered. Returning a half-built `file` view
    // would render it as an empty FILE tab, which is worse than saying nothing.
    expect(tabPayloadView(create(TabPayloadSchema, { workingDir: '/repo' }))).toBeNull()
  })
})
