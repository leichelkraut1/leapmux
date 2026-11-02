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

function event(payload: Record<string, unknown>) {
  return { timestamp: '2026-10-28T01:00:00Z', type: 'event_msg', payload: { turn_id: 'generated-turn', ...payload } }
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

  it('reads persisted core item events with native timing fields through the normal row pipeline', () => {
    const records = nativeRecords([
      event({ type: 'item_started', thread_id: session, item: { type: 'AgentMessage', id: 'generated-answer', content: [] }, started_at_ms: 1793586737748 }),
      event({ type: 'item_completed', thread_id: session, item: { type: 'AgentMessage', id: 'generated-answer', content: [{ type: 'Text', text: 'Generated native answer' }] }, started_at_ms: 1793586737748, completed_at_ms: 1793586737748 }),
      event({ type: 'item_completed', item: { type: 'Reasoning', id: 'generated-reasoning', summary_text: ['Generated visible summary'], raw_content: [] }, completed_at_ms: 1793586737748 }),
      event({ type: 'item_completed', item: { type: 'Plan', id: 'generated-plan', text: 'Generated proposed plan' }, completed_at_ms: 1793586737748 }),
      event({ type: 'item_completed', item: { type: 'UserMessage', id: 'generated-user', content: [{ type: 'text', text: 'Generated native question', text_elements: [] }] }, completed_at_ms: 1793586737748 }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(5)
    expect(messages.map(message => prepareChatRow(message).extraction)).toMatchObject([
      { kind: 'row', row: { kind: 'hidden' } },
      { kind: 'row', row: { kind: 'assistant-text', text: 'Generated native answer' } },
      { kind: 'row', row: { kind: 'assistant-thinking', text: '- Generated visible summary' } },
      { kind: 'row', row: { kind: 'assistant-plan', text: 'Generated proposed plan' } },
      { kind: 'row', row: { kind: 'user', text: 'Generated native question' } },
    ])
    expect(messages.map(message => message.content)).toEqual(records.map(record => record.nativeJson))
    expect(messages.every(message => message.completion === 0 && message.transcriptOnly)).toBe(true)
  })

  it('deduplicates proved core text mirrors without losing distinct turns or future content', () => {
    const records = nativeRecords([
      event({ type: 'item_started', item: { type: 'AgentMessage', id: 'generated-answer', content: [{ type: 'Text', text: 'Generated answer' }] } }),
      event({ type: 'agent_message', message: 'Generated answer' }),
      event({ type: 'item_completed', item: { type: 'AgentMessage', id: 'generated-answer', content: [{ type: 'Text', text: 'Generated answer' }] } }),
      response({ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Generated answer' }] }),
      event({ type: 'item_completed', turn_id: 'next-turn', item: { type: 'AgentMessage', id: 'next-answer', content: [{ type: 'Text', text: 'Generated answer' }] } }),
      event({ type: 'item_completed', turn_id: 'next-turn', item: { type: 'AgentMessage', id: 'future-answer', content: [{ type: 'FutureText', text: 'Generated answer' }] } }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(3)
    expect(messages.map(message => prepareChatRow(message).extraction.kind)).toEqual(['row', 'row', 'unsupported'])
    expect(messages.map(message => message.content)).toEqual([records[3]!.nativeJson, records[4]!.nativeJson, records[5]!.nativeJson])
    // Loading a response mirror removes only its duplicated core row; unrelated
    // message IDs do not depend on how much history happens to be loaded.
    expect(projectCodexNativeConversation(records.slice(4), format, session).map(message => message.id)).toEqual(messages.slice(1).map(message => message.id))
  })

  it('reads core command outcomes and output without treating completion as success', () => {
    const records = nativeRecords([
      event({ type: 'item_started', item: { type: 'CommandExecution', id: 'generated-command', command: ['/bin/zsh', '-lc', 'printf generated'], cwd: '/harmless', status: 'in_progress' }, started_at_ms: 1000 }),
      event({ type: 'item_completed', item: { type: 'CommandExecution', id: 'generated-command', command: ['/bin/zsh', '-lc', 'printf generated'], cwd: '/harmless', status: 'failed', aggregated_output: 'Generated stderr', exit_code: 2, duration: { secs: 1, nanos: 500000000 } }, started_at_ms: 1000, completed_at_ms: 2500 }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    const scenario = createTranscriptScenario({ archive: messages })
    expect(messages.map(message => [message.spanId, message.spanType])).toEqual([['generated-command', 'commandExecution'], ['generated-command', 'commandExecution']])
    expect(scenario.toolRow(messages[1]!.id)).toMatchObject({
      role: 'result',
      call: { kind: 'execute', status: 'failed', request: { command: 'printf generated', cwd: '/harmless' }, result: { commands: [{ output: 'Generated stderr', exitCode: 2, durationMs: 1500 }] } },
    })
    expect(messages[1]!.content).toEqual(records[1]!.nativeJson)
  })

  it('reads native dynamic tool results without renaming user argument or result keys', () => {
    const records = nativeRecords([
      event({ type: 'item_completed', item: { type: 'DynamicToolCall', id: 'generated-dynamic', namespace: 'generated', tool: 'inspect', arguments: { keep_snake_case: 'generated input' }, status: 'completed', success: true, content_items: [{ type: 'inputText', text: 'Generated tool output' }], duration: { secs: 0, nanos: 10000000 } }, started_at_ms: 1000, completed_at_ms: 1010 }),
    ])
    const message = projectCodexNativeConversation(records, format, session)[0]!
    expect(createTranscriptScenario({ archive: [message] }).toolRow(message.id)).toMatchObject({
      role: 'result',
      call: { kind: 'mcp', status: 'completed', request: { server: 'generated', tool: 'inspect', args: { keep_snake_case: 'generated input' } }, result: { content: [{ type: 'text', text: 'Generated tool output' }], durationMs: 10 } },
    })
    expect(message.content).toEqual(records[0]!.nativeJson)
  })

  it('renders native end and interruption markers while suppressing turn-start telemetry', () => {
    const records = nativeRecords([
      event({ type: 'task_started', model_context_window: 1000 }),
      event({ type: 'task_complete', last_agent_message: 'Generated final answer' }),
      event({ type: 'turn_aborted', turn_id: 'aborted-turn', reason: 'interrupted' }),
      event({ type: 'turn_complete', turn_id: 'failed-turn', error: { message: 'Generated native error' } }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(4)
    expect(messages.map(message => prepareChatRow(message).extraction)).toMatchObject([
      { kind: 'row', row: { kind: 'assistant-text', text: 'Generated final answer' } },
      { kind: 'row', row: { kind: 'divider', divider: { label: 'Turn ended' } } },
      { kind: 'row', row: { kind: 'divider', divider: { label: 'Turn interrupted' } } },
      { kind: 'row', row: { kind: 'divider', divider: { isError: true } } },
    ])
    const withAnswer = nativeRecords([
      event({ type: 'agent_message', message: 'Generated final answer' }),
      event({ type: 'task_complete', last_agent_message: 'Generated final answer' }),
    ])
    expect(projectCodexNativeConversation(withAnswer, format, session).map(message => prepareChatRow(message).extraction)).toMatchObject([
      { kind: 'row', row: { kind: 'assistant-text' } },
      { kind: 'row', row: { kind: 'divider' } },
    ])
  })

  it('keeps unknown native records and malformed known items raw-visible while hiding encrypted-only thinking', () => {
    const records = nativeRecords([
      response({ type: 'future-native-item', payload: { text: 'Keep exact future data' } }),
      response({ type: 'reasoning', encrypted_content: 'Opaque native reasoning bytes', summary: [] }),
      event({ type: 'item_completed', item: { type: 'FutureItem', id: 'future-item', text: 'Generated future data' } }),
      event({ type: 'item_completed', item: { type: 'CommandExecution', id: 'malformed-command', command: ['generated'], aggregated_output: 'Generated output without declared status' } }),
    ])
    const messages = projectCodexNativeConversation(records, format, session)
    expect(messages).toHaveLength(4)
    expect(messages.map(message => prepareChatRow(message).extraction.kind)).toEqual(['unsupported', 'row', 'unsupported', 'unsupported'])
    expect(prepareChatRow(messages[1]!).extraction).toMatchObject({ kind: 'row', row: { kind: 'hidden' } })
    expect(messages[0]!.content).toEqual(records[0]!.nativeJson)
    expect(messages[1]!.content).toEqual(records[1]!.nativeJson)
  })

  it('rejects conflicting record identities, overlapping source offsets, and foreign session metadata', () => {
    const records = nativeRecords([response({ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'One' }] }), response({ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Two' }] })])
    expect(projectCodexNativeConversation([records[0]!, { ...records[1]!, id: records[0]!.id }], format, session)).toEqual([])
    expect(projectCodexNativeConversation([records[0]!, { ...records[1]!, seq: records[0]!.seq + 1n }], format, session)).toEqual([])
    expect(projectCodexNativeConversation(nativeRecords([{ type: 'session_meta', payload: { id: 'foreign' } }]), format, session)).toEqual([])
    expect(projectCodexNativeConversation(nativeRecords([event({ type: 'item_completed', thread_id: 'foreign', item: { type: 'AgentMessage', id: 'foreign-message', content: [{ type: 'Text', text: 'Foreign text' }] } })]), format, session)).toEqual([])
    expect(projectCodexNativeConversation(records, 'unknown-format', session)).toEqual([])
    expect(projectCodexNativeConversation([records[0]!, records[0]!], format, session)).toHaveLength(1)
  })
})
