import type { NativeConversationRecord } from '../../nativeConversation'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { NATIVE_CONVERSATION_FORMAT } from '~/generated/contracts/native-conversations'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'

/**
 * Project saved envelopes into render frames, without replaying a live provider
 * handler or manufacturing controls. Each native block gets its own frame so a
 * thinking/text/tool mixture does not lose the blocks after the first one.
 * Anthropic's SDK v0.2.170 reconstructs branches before paging the full file:
 * https://github.com/anthropics/claude-agent-sdk-python/blob/v0.2.170/src/claude_agent_sdk/_internal/sessions.py
 * A loaded window cannot certify that branch; retain stored records rather
 * than silently dropping parallel tools, forks or compacted summaries.
 */
export function projectClaudeNativeConversation(
  records: readonly NativeConversationRecord[],
  format: string,
  sessionId: string,
): AgentChatMessage[] {
  if (format !== NATIVE_CONVERSATION_FORMAT.ClaudeJSONL || !sessionId)
    throw new Error('Unsupported Claude conversation format')

  const decoder = new TextDecoder('utf-8', { fatal: true })
  const encoder = new TextEncoder()
  const toolNames = new Map<string, string>()
  const seen = new Map<string, { seq: bigint, raw: string }>()
  const envelopes: { record: NativeConversationRecord, envelope: Record<string, unknown> }[] = []
  for (const record of records) {
    const raw = decoder.decode(record.nativeJson)
    const previous = seen.get(record.id)
    if (previous) {
      if (previous.seq !== record.seq || previous.raw !== raw)
        throw new Error('Inconsistent Claude conversation record')
      continue
    }
    const envelope: unknown = JSON.parse(raw)
    if (!record.id || record.seq < 1n || !isObject(envelope)
      || (envelope.sessionId !== undefined && envelope.sessionId !== sessionId)
      || envelope.isSidechain === true) {
      throw new Error('Inconsistent Claude conversation identity')
    }
    seen.set(record.id, { seq: record.seq, raw })
    envelopes.push({ record, envelope })
    const message = isObject(envelope.message) ? envelope.message : undefined
    if (message && Array.isArray(message.content)) {
      for (const block of message.content) {
        if (isObject(block) && block.type === 'tool_use'
          && typeof block.id === 'string' && typeof block.name === 'string') {
          const previousName = toolNames.get(block.id)
          if (previousName !== undefined && previousName !== block.name)
            throw new Error('Inconsistent Claude conversation tool identity')
          toolNames.set(block.id, block.name)
        }
      }
    }
  }
  envelopes.sort((a, b) => a.record.seq < b.record.seq ? -1 : a.record.seq > b.record.seq ? 1 : 0)

  const messages: AgentChatMessage[] = []
  let previousSeq = 0n
  for (const { record, envelope } of envelopes) {
    const nativeMessage = isObject(envelope.message) ? envelope.message : undefined
    const blocks = nativeMessage && Array.isArray(nativeMessage.content) && nativeMessage.content.length > 0
      ? nativeMessage.content
      : [undefined]
    // A record's one-based byte offset leaves enough space for its own blocks;
    // unlike a fixed multiplier this also supports unusually large block lists.
    if (blocks.length > record.nativeJson.byteLength)
      throw new Error('Invalid Claude conversation block count')
    blocks.forEach((block: unknown, index: number) => {
      const seq = record.seq + BigInt(index)
      if (seq <= previousSeq)
        throw new Error('Overlapping Claude conversation records')
      previousSeq = seq
      const projected = block === undefined || !nativeMessage
        ? envelope
        : { ...envelope, message: { ...nativeMessage, content: [block] } }
      const tool = isObject(block) ? block : undefined
      const spanId = tool?.type === 'tool_use' && typeof tool.id === 'string'
        ? tool.id
        : tool?.type === 'tool_result' && typeof tool.tool_use_id === 'string'
          ? tool.tool_use_id
          : ''
      const spanType = tool?.type === 'tool_use' && typeof tool.name === 'string'
        ? tool.name
        : toolNames.get(spanId) ?? ''
      const parentSpanId = typeof envelope.parent_tool_use_id === 'string' ? envelope.parent_tool_use_id : ''
      messages.push(create(AgentChatMessageSchema, {
        id: `${record.id}:block:${index}`,
        seq,
        content: block === undefined ? record.nativeJson : encoder.encode(JSON.stringify(projected)),
        contentCompression: ContentCompression.NONE,
        supplementalContentCompression: ContentCompression.NONE,
        source: envelope.type === 'user' ? MessageSource.USER : MessageSource.AGENT,
        agentProvider: AgentProvider.CLAUDE_CODE,
        agentSessionId: sessionId,
        createdAt: typeof envelope.timestamp === 'string' ? envelope.timestamp : '',
        parentSpanId,
        depth: parentSpanId ? 1 : 0,
        spanId,
        spanType,
        spanColor: -1,
        transcriptOnly: true,
      }))
    })
  }
  return messages
}
