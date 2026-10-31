import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { nativeConversationFromParams, nativeConversationKey, nativeConversationRouteProvider } from './nativeConversation'

const session = '11111111-2222-3333-4444-555555555555'
const workers = [{ workerId: 'registered', workerName: 'Test PC' }]
const params = { dashboard: 'agents', externalWorker: 'registered', externalProvider: 'codex', externalSession: session }

describe('native conversation links', () => {
  it('uses only a known computer and exact supported provider/session identity', () => {
    const ref = nativeConversationFromParams(params, workers)
    expect(ref).toEqual({ ...workers[0], agentProvider: AgentProvider.CODEX, sessionId: session })
    expect(nativeConversationRouteProvider(AgentProvider.CODEX)).toBe('codex')
    expect(nativeConversationRouteProvider(AgentProvider.CLAUDE_CODE)).toBe('claude')
    expect(nativeConversationRouteProvider(AgentProvider.UNSPECIFIED)).toBeUndefined()
    expect(nativeConversationFromParams({ ...params, externalProvider: 'claude' }, workers)?.agentProvider).toBe(AgentProvider.CLAUDE_CODE)
  })

  it('rejects unknown computers, inherited provider names, paths, duplicate params and malformed UUIDs', () => {
    for (const bad of [
      { dashboard: 'other' },
      { externalWorker: 'unknown' },
      { externalWorker: '../registered' },
      { externalProvider: 'constructor' },
      { externalProvider: '__proto__' },
      { externalProvider: 'unknown' },
      { externalSession: '../history.jsonl' },
      { externalSession: 'not-a-uuid' },
      { externalSession: [session, session] },
    ]) {
      expect(nativeConversationFromParams({ ...params, ...bad }, workers)).toBeUndefined()
    }
  })

  it('scopes an identical session ID to its computer and provider', () => {
    const base = { ...workers[0]!, agentProvider: AgentProvider.CODEX, sessionId: session }
    expect(nativeConversationKey(base)).not.toBe(nativeConversationKey({ ...base, workerId: 'other' }))
    expect(nativeConversationKey(base)).not.toBe(nativeConversationKey({ ...base, agentProvider: AgentProvider.CLAUDE_CODE }))
    expect(nativeConversationKey(base)).toBe(nativeConversationKey({ ...base, workerName: 'Renamed' }))
  })
})
