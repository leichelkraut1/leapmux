import type { NativeConversationRecord } from '../../nativeConversation'
import { describe, expect, it } from 'vitest'
import { NATIVE_CONVERSATION_FORMAT } from '~/generated/contracts/native-conversations'
import { AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { providerRow } from '~/test-support/toolCallFixture'
import { extractedRow } from '../../rowExtraction'
import { prepareChatRow } from '../../rowPreparation'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { classifyClaudeCodeMessage } from './classification'
import { claudeToolResultImages } from './extractors/image'
import { projectClaudeNativeConversation } from './nativeConversation'
import './plugin'

const format = NATIVE_CONVERSATION_FORMAT.ClaudeJSONL
const session = 'generated-session'
const encoder = new TextEncoder()
const decoder = new TextDecoder()

function records(...envelopes: Record<string, unknown>[]): NativeConversationRecord[] {
  let offset = 0n
  return envelopes.map((envelope) => {
    const nativeJson = encoder.encode(JSON.stringify({ sessionId: session, ...envelope }))
    const record = { id: `generated:${offset}`, seq: offset + 1n, nativeJson }
    offset += BigInt(nativeJson.byteLength + 1)
    return record
  })
}

describe('Claude read-only native conversation projection', () => {
  it('registers the passive reader separately from live controls', () => {
    expect(providerFor(AgentProvider.CLAUDE_CODE)?.transcript.projectNativeConversation)
      .toBe(projectClaudeNativeConversation)
  })

  it('decodes projected mixed native frames through the actual chat preparation pipeline', () => {
    const frames = projectClaudeNativeConversation(records(
      { type: 'user', message: { role: 'user', content: [
        { type: 'text', text: 'Generated image question' },
        { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AA==' } },
      ] } },
      { type: 'assistant', message: { role: 'assistant', content: [
        { type: 'text', text: 'Generated latest answer' },
        { type: 'tool_use', id: 'generated-read', name: 'Read', input: { file_path: 'generated.txt' } },
      ] } },
      { type: 'user', message: { role: 'user', content: [
        { type: 'tool_result', tool_use_id: 'generated-read', content: 'Generated file contents' },
      ] } },
    ), format, session)
    expect(frames).toHaveLength(5)
    const rows = frames.map((frame) => {
      expect(frame.contentCompression).toBe(ContentCompression.NONE)
      expect(frame.supplementalContentCompression).toBe(ContentCompression.NONE)
      const parsed = parseMessageContent(frame)
      expect(parsed.contentDecodeFailed).not.toBe(true)
      expect(parsed.rawText).toBe(decoder.decode(frame.content))
      expect(parsed.parentObject).toBeDefined()
      const result = prepareChatRow(frame)
      expect(result.extraction.kind).toBe('row')
      return extractedRow(result.extraction)
    })
    expect(rows[0]).toEqual({ kind: 'user', text: 'Generated image question', attachments: [] })
    expect(rows[1]).toEqual({ kind: 'user', text: '', attachments: [{ mimeType: 'image/png', image: { data: 'AA==', mimeType: 'image/png' } }] })
    expect(rows[2]).toEqual({ kind: 'assistant-text', text: 'Generated latest answer' })
    expect(rows[3]).toMatchObject({ kind: 'tool', call: { kind: 'read' } })
    expect(rows[4]).toMatchObject({ kind: 'tool', call: { kind: 'read' } })
    const supplemented = { ...frames[0]!, supplementalContent: encoder.encode(JSON.stringify({ provider: { generated: 'supplement' } })) }
    expect(parseMessageContent(supplemented).supplementalContent).toEqual({ generated: 'supplement' })
  })

  it('preserves every native block, identity and tool span across a mixed saved turn', () => {
    const raw = records(
      { type: 'user', uuid: 'user-uuid', message: { role: 'user', content: 'Inspect this generated fixture' } },
      {
        type: 'assistant',
        uuid: 'assistant-uuid',
        parentUuid: 'user-uuid',
        timestamp: '2026-10-28T12:00:00Z',
        message: {
          id: 'native-message',
          role: 'assistant',
          content: [
            { type: 'thinking', thinking: 'Check the generated contents' },
            { type: 'text', text: '```ts\nconst answer = 42\n```' },
            { type: 'tool_use', id: 'tool-one', name: 'Read', input: { file_path: 'generated.txt' } },
            { type: 'tool_use', id: 'tool-two', name: 'Bash', input: { command: 'printf generated' } },
          ],
        },
      },
      {
        type: 'user',
        uuid: 'result-uuid',
        message: {
          role: 'user',
          content: [
            { type: 'tool_result', tool_use_id: 'tool-two', content: 'generated stdout' },
            { type: 'tool_result', tool_use_id: 'tool-one', content: [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AA==' } }] },
          ],
        },
      },
    )
    const projected = projectClaudeNativeConversation(raw, format, session)
    expect(projected).toHaveLength(7)
    expect(projected.map(message => message.seq)).toEqual([
      raw[0]!.seq,
      raw[1]!.seq,
      raw[1]!.seq + 1n,
      raw[1]!.seq + 2n,
      raw[1]!.seq + 3n,
      raw[2]!.seq,
      raw[2]!.seq + 1n,
    ])
    expect(projected.map(message => message.spanId)).toEqual(['', '', '', 'tool-one', 'tool-two', 'tool-two', 'tool-one'])
    expect(projected.map(message => message.spanType)).toEqual(['', '', '', 'Read', 'Bash', 'Bash', 'Read'])
    expect(projected.every(message => message.agentSessionId === session && message.transcriptOnly)).toBe(true)
    expect(projected[0]!.source).toBe(MessageSource.USER)
    expect(projected[1]!.source).toBe(MessageSource.AGENT)
    // parentUuid is a conversation branch, never the parent tool scope.
    expect(projected[1]!.parentSpanId).toBe('')
    const thinking = JSON.parse(decoder.decode(projected[1]!.content))
    expect(thinking.uuid).toBe('assistant-uuid')
    expect(thinking.message.id).toBe('native-message')
    expect(classifyClaudeCodeMessage(input(thinking))).toEqual({ kind: 'assistant_thinking' })
    const text = JSON.parse(decoder.decode(projected[2]!.content))
    expect(providerRow(AgentProvider.CLAUDE_CODE, text)).toEqual({ kind: 'assistant-text', text: '```ts\nconst answer = 42\n```' })
    const image = JSON.parse(decoder.decode(projected[6]!.content))
    expect(claudeToolResultImages(image, 'Read', undefined)).toEqual([{ data: 'AA==', mimeType: 'image/png' }])
  })

  it('shows native user inline images without file reads or implicit remote loads', () => {
    const raw = records({ type: 'user', message: { content: [
      { type: 'text', text: 'Generated screenshot' },
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AA==' } },
      { type: 'image', source: { type: 'url', url: 'https://example.invalid/generated.png' }, uri: 'file:///generated.png' },
    ] } })
    const frames = projectClaudeNativeConversation(raw, format, session)
    const row = (index: number) => providerRow(AgentProvider.CLAUDE_CODE, JSON.parse(decoder.decode(frames[index]!.content)))
    expect(row(0)).toEqual({ kind: 'user', text: 'Generated screenshot', attachments: [] })
    expect(row(1)).toEqual({ kind: 'user', text: '', attachments: [{ mimeType: 'image/png', image: { data: 'AA==', mimeType: 'image/png' } }] })
    expect(row(2)).toEqual({ kind: 'user', text: '', attachments: [{ image: {} }] })
    expect(providerRow(AgentProvider.CLAUDE_CODE, JSON.parse(decoder.decode(raw[0]!.nativeJson))))
      .toEqual({ kind: 'user', text: 'Generated screenshot', attachments: [
        { mimeType: 'image/png', image: { data: 'AA==', mimeType: 'image/png' } },
        { image: {} },
      ] })
  })

  it('keeps unknown native blocks and storage records available to the raw read-only fallback', () => {
    const raw = records(
      { type: 'file-history-snapshot', snapshot: { trackedFileBackups: {} } },
      { type: 'assistant', message: { content: [{ type: 'future-block', generatedField: 'kept' }] } },
    )
    const frames = projectClaudeNativeConversation(raw, format, session)
    expect(frames[0]!.content).toBe(raw[0]!.nativeJson)
    const unknown = JSON.parse(decoder.decode(frames[1]!.content))
    expect(unknown.message.content).toEqual([{ type: 'future-block', generatedField: 'kept' }])
    expect(classifyClaudeCodeMessage(input(unknown))).toEqual({ kind: 'unknown' })
  })

  it('renders saved assistant strings as text while preserving their native envelope', () => {
    const raw = records({ type: 'assistant', message: { role: 'assistant', content: 'generated stored assistant text' } })
    const frame = projectClaudeNativeConversation(raw, format, session)[0]!
    expect(frame.content).toBe(raw[0]!.nativeJson)
    expect(providerRow(AgentProvider.CLAUDE_CODE, JSON.parse(decoder.decode(frame.content))))
      .toEqual({ kind: 'assistant-text', text: 'generated stored assistant text' })
  })

  it('keeps IDs stable across page overlap and appended records without a fixed block multiplier', () => {
    const raw = records(
      { type: 'assistant', message: { content: Array.from({ length: 1100 }, (_, index) => ({ type: 'text', text: `generated ${index}` })) } },
      { type: 'user', message: { content: 'generated next' } },
    )
    const first = projectClaudeNativeConversation(raw.slice(0, 1), format, session)
    const appended = projectClaudeNativeConversation([raw[1]!, raw[0]!, raw[0]!], format, session)
    expect(appended).toHaveLength(1101)
    expect(appended.slice(0, first.length)).toEqual(first)
    expect(appended[1099]!.id).toBe(`${raw[0]!.id}:block:1099`)
    expect(appended[1100]!.seq > appended[1099]!.seq).toBe(true)
  })

  it('preserves stored forks, repeated native UUIDs and compact summaries without guessing a branch from a partial page', () => {
    const raw = records(
      { type: 'user', uuid: 'root', parentUuid: null, message: { content: 'generated root' } },
      { type: 'assistant', uuid: 'fork-old', parentUuid: 'root', message: { content: [{ type: 'text', text: 'generated earlier fork' }] } },
      { type: 'assistant', uuid: 'fork-new', parentUuid: 'root', message: { content: [{ type: 'text', text: 'generated newer fork' }] } },
      { type: 'assistant', uuid: 'fork-new', parentUuid: 'root', message: { content: [{ type: 'text', text: 'generated repeated native UUID' }] } },
      { type: 'system', uuid: 'compact', parentUuid: null, logicalParentUuid: 'fork-new', subtype: 'compact_boundary' },
      { type: 'user', uuid: 'summary', parentUuid: 'compact', isCompactSummary: true, message: { content: 'generated compact summary' } },
    )
    const frames = projectClaudeNativeConversation(raw, format, session)
    expect(frames).toHaveLength(raw.length)
    expect(new Set(frames.map(frame => frame.id)).size).toBe(raw.length)
    expect(frames.map(frame => JSON.parse(decoder.decode(frame.content)).uuid))
      .toEqual(['root', 'fork-old', 'fork-new', 'fork-new', 'compact', 'summary'])
    const summary = JSON.parse(decoder.decode(frames[5]!.content))
    expect(summary.isCompactSummary).toBe(true)
    expect(providerRow(AgentProvider.CLAUDE_CODE, summary))
      .toEqual({ kind: 'user', text: 'generated compact summary', attachments: [] })
    expect(projectClaudeNativeConversation(raw.slice(4), format, session).map(frame => frame.id))
      .toEqual(frames.slice(4).map(frame => frame.id))
  })

  it('retains parallel tool branches sharing an API response and pairs every result by its own tool ID', () => {
    const raw = records(
      { type: 'assistant', uuid: 'call-a', parentUuid: 'generated-root', message: { id: 'shared-response', content: [{ type: 'tool_use', id: 'parallel-a', name: 'Read', input: { file_path: 'generated-a.txt' } }] } },
      { type: 'assistant', uuid: 'call-b', parentUuid: 'call-a', message: { id: 'shared-response', content: [{ type: 'tool_use', id: 'parallel-b', name: 'Bash', input: { command: 'printf generated' } }] } },
      { type: 'user', uuid: 'result-b', parentUuid: 'call-b', sourceToolAssistantUUID: 'call-b', message: { content: [{ type: 'tool_result', tool_use_id: 'parallel-b', content: 'generated stdout' }] } },
      { type: 'user', uuid: 'result-a', parentUuid: 'call-a', sourceToolAssistantUUID: 'call-a', message: { content: [{ type: 'tool_result', tool_use_id: 'parallel-a', content: 'generated file contents' }] } },
      { type: 'assistant', uuid: 'continued', parentUuid: 'result-a', message: { id: 'next-response', content: [{ type: 'text', text: 'generated continuation' }] } },
    )
    const frames = projectClaudeNativeConversation(raw, format, session)
    expect(frames).toHaveLength(5)
    expect(frames.map(frame => [frame.spanId, frame.spanType]))
      .toEqual([['parallel-a', 'Read'], ['parallel-b', 'Bash'], ['parallel-b', 'Bash'], ['parallel-a', 'Read'], ['', '']])
    expect(JSON.parse(decoder.decode(frames[2]!.content)).sourceToolAssistantUUID).toBe('call-b')
  })

  it('refuses foreign sessions, sidechains, changed overlapping rows and invalid record ordering', () => {
    const native = { type: 'user', message: { content: 'generated' } }
    expect(() => projectClaudeNativeConversation(records(native), 'other-format', session)).toThrow()
    expect(() => projectClaudeNativeConversation(records({ ...native, sessionId: 'foreign' }), format, session)).toThrow()
    expect(() => projectClaudeNativeConversation(records({ ...native, isSidechain: true }), format, session)).toThrow()
    const original = records(native)[0]!
    expect(() => projectClaudeNativeConversation([original, { ...original, nativeJson: encoder.encode('{}') }], format, session)).toThrow()
    expect(() => projectClaudeNativeConversation([original, { ...original, id: 'different' }], format, session)).toThrow()
  })
})
