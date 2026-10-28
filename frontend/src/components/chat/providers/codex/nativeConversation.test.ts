import type { NativeConversationRecord } from '../../nativeConversation'
import { describe, expect, it } from 'vitest'
import { NATIVE_CONVERSATION_FORMAT } from '~/generated/contracts/native-conversations'
import { AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { pngBase64 } from '~/test-support/pngFixture'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import { prepareChatRow, prepareMessage } from '../../rowPreparation'
import { projectCodexNativeConversation } from './nativeConversation'
import './plugin'

const session = '11111111-1111-4111-8111-111111111111'
const format = NATIVE_CONVERSATION_FORMAT.CodexRolloutJSONL
const encoder = new TextEncoder()

function nativeRecords(values: unknown[]): NativeConversationRecord[] {
  let offset = 0n
  return values.map((value) => {
    const nativeJson = encoder.encode(JSON.stringify(value))
    const record = { id: `generation:${offset}`, seq: offset + 1n, nativeJson }
    offset += BigInt(nativeJson.length + 1)
    return record
  })
}

function response(payload: Record<string, unknown>) {
  return { timestamp: '2026-10-28T01:00:00Z', type: 'response_item', payload }
}

describe('projectCodexNativeConversation', () => {
  it('renders user and assistant text while retaining exact raw source bytes', () => {
    const records = nativeRecords([
      { type: 'session_meta', payload: { id: session, cwd: '/harmless' } },
      response({ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Harmless question' }] }),
      response({ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Harmless answer' }] }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(2)
    const user = messages[0]!
    const assistant = messages[1]!
    expect(user).toMatchObject({ source: MessageSource.USER, agentProvider: AgentProvider.CODEX, agentSessionId: session, transcriptOnly: true, contentCompression: ContentCompression.NONE, completion: 0 })
    expect(user.content).toEqual(records[1]!.nativeJson)
    expect(assistant.content).toEqual(records[2]!.nativeJson)
    expect(parseMessageContent(assistant).rawText).toBe(new TextDecoder().decode(records[2]!.nativeJson))
    expect(prepareChatRow(user).extraction).toMatchObject({ kind: 'row', row: { kind: 'user', text: 'Harmless question' } })
    expect(prepareChatRow(assistant).extraction).toMatchObject({ kind: 'row', row: { kind: 'assistant-text', text: 'Harmless answer' } })
  })

  it('deduplicates text mirrors without dropping repeated responses in different turns', () => {
    const records = nativeRecords([
      { type: 'turn_context', payload: { turn_id: 'turn-1' } },
      { type: 'event_msg', payload: { type: 'user_message', message: 'Again' } },
      response({ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Again' }] }),
      { type: 'turn_context', payload: { turn_id: 'turn-2' } },
      { type: 'event_msg', payload: { type: 'user_message', message: 'Again' } },
      response({ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Again' }] }),
    ])
    const messages = projectCodexNativeConversation([...records.slice(3), ...records.slice(0, 3)], format, session)
    expect(messages).toHaveLength(2)
    expect(messages[0]!.seq).toBeLessThan(messages[1]!.seq)
    expect(messages.map(message => prepareMessage(message).resolved.parentObject?.content)).toEqual(['Again', 'Again'])
  })

  it('keeps a request without native output unstated and never invents an event or completion', () => {
    const payload = { type: 'function_call', call_id: 'native-call', name: 'exec_command', arguments: '{"cmd":"harmless probe"}' }
    const records = nativeRecords([response(payload)])
    const message = projectCodexNativeConversation(records, format, session)[0]!
    const prepared = prepareMessage(message)
    expect(prepared.resolved.parentObject).toMatchObject({ nativeHistory: true, threadId: session, item: payload })
    expect(prepared.resolved.parentObject?.method).toBeUndefined()
    expect(prepared.resolved.parentObject?.status).toBeUndefined()
    expect(message.completion).toBe(0)
    const scenario = createTranscriptScenario({ archive: [message] })
    expect(scenario.toolRow(message.id).call).toMatchObject({ kind: 'mcp', name: 'exec_command', status: 'unstated', request: { args: { cmd: 'harmless probe' } } })
  })

  it('joins native call and output across a loaded page edge using real span lookup', async () => {
    const records = nativeRecords([
      { type: 'turn_context', payload: { turn_id: 'turn-1' } },
      response({ type: 'function_call', call_id: 'native-call', name: 'exec_command', arguments: '{"cmd":"harmless probe"}' }),
      response({ type: 'function_call_output', call_id: 'native-call', output: 'Exact recorded output' }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    const result = messages[1]!
    const scenario = createTranscriptScenario({ archive: messages, windowIds: [result.id] })
    await scenario.loadSpan(result.id)
    expect(scenario.toolRow(result.id)).toMatchObject({
      role: 'result',
      call: { kind: 'mcp', name: 'exec_command', request: { args: { cmd: 'harmless probe' } }, result: { content: [{ type: 'text', text: 'Exact recorded output' }] } },
    })
    expect(messages.every(message => message.completion === 0)).toBe(true)
  })

  it('does not borrow request fields from a different native turn', () => {
    const records = nativeRecords([
      { type: 'turn_context', payload: { turn_id: 'old-turn' } },
      response({ type: 'function_call', call_id: 'reused-call', name: 'foreign-tool', arguments: '{"foreign":true}' }),
      { type: 'turn_context', payload: { turn_id: 'new-turn' } },
      response({ type: 'function_call_output', call_id: 'reused-call', output: 'Own output' }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    const scenario = createTranscriptScenario({ archive: messages })
    expect(scenario.toolRow(messages[1]!.id).call).toMatchObject({ kind: 'mcp', request: { tool: 'Function output', args: {} }, result: { content: [{ type: 'text', text: 'Own output' }] } })
  })

  it('renders safe embedded user images while retaining external references as raw-visible rows', () => {
    const url = `data:image/png;base64,${pngBase64(16, 16)}`
    const records = nativeRecords([
      response({ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'See image' }, { type: 'input_image', image_url: url }] }),
      response({ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'External image' }, { type: 'input_image', image_url: 'https://example.invalid/private.png' }] }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(3)
    expect(prepareChatRow(messages[0]!).extraction).toMatchObject({ kind: 'row', row: { kind: 'user', attachments: [{ image: { url } }] } })
    expect(prepareChatRow(messages[1]!).extraction).toMatchObject({ kind: 'row', row: { kind: 'user', attachments: [] } })
    expect(prepareChatRow(messages[2]!).extraction.kind).toBe('unsupported')
    expect(messages[1]!.seq).toBeLessThan(messages[2]!.seq)
  })

  it('shows safe inline function-output images and explicit native failure', () => {
    const url = `data:image/png;base64,${pngBase64(16, 16)}`
    const records = nativeRecords([response({ type: 'function_call_output', call_id: 'image-output', is_error: true, output: [{ type: 'input_image', image_url: url }, { type: 'input_text', text: 'Native failure detail' }] })])
    const message = projectCodexNativeConversation(records, format, session)[0]!
    const scenario = createTranscriptScenario({ archive: [message] })
    expect(scenario.toolRow(message.id).call).toMatchObject({ kind: 'mcp', status: 'failed', result: { content: [{ type: 'image', source: { url } }, { type: 'text', text: 'Native failure detail' }] } })
  })

  it('keeps unknown native records and opaque reasoning visible in raw form', () => {
    const records = nativeRecords([
      response({ type: 'future-native-item', payload: { text: 'Keep exact future data' } }),
      response({ type: 'reasoning', encrypted_content: 'Opaque native reasoning bytes', summary: [] }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(2)
    expect(messages.every(message => prepareChatRow(message).extraction.kind === 'unsupported')).toBe(true)
    expect(messages[0]!.content).toEqual(records[0]!.nativeJson)
  })

  it('rejects conflicting record identities, overlapping source offsets, and foreign session metadata', () => {
    const records = nativeRecords([response({ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'One' }] }), response({ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Two' }] })])
    expect(projectCodexNativeConversation([records[0]!, { ...records[1]!, id: records[0]!.id }], format, session)).toEqual([])
    expect(projectCodexNativeConversation([records[0]!, { ...records[1]!, seq: records[0]!.seq + 1n }], format, session)).toEqual([])
    expect(projectCodexNativeConversation(nativeRecords([{ type: 'session_meta', payload: { id: 'foreign' } }]), format, session)).toEqual([])
    expect(projectCodexNativeConversation(records, 'unknown-format', session)).toEqual([])
    expect(projectCodexNativeConversation([records[0]!, records[0]!], format, session)).toHaveLength(1)
  })
})
