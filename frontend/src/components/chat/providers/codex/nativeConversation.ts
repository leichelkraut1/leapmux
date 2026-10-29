import type { UserMessageAttachment } from '../../model/row'
import type { NativeConversationRecord } from '../../nativeConversation'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { create } from '@bufbuild/protobuf'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { NATIVE_CONVERSATION_FORMAT } from '~/generated/contracts/native-conversations'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

interface NativeEntry {
  record: NativeConversationRecord
  envelope: Record<string, unknown>
  payload: Record<string, unknown>
  turnId: string
  index: number
}

const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true })
const encoder = new TextEncoder()
const maxSeq = 9223372036854775807n
const inlineImage = /^data:(image\/(?:png|jpeg|gif|webp));base64,[A-Za-z0-9+/]+={0,2}$/

function parsedEntries(records: readonly NativeConversationRecord[], sessionId: string): NativeEntry[] {
  const unique = new Map<string, NativeConversationRecord>()
  for (const record of records) {
    const previous = unique.get(record.id)
    if (!record.id || record.seq <= 0n || (previous && (previous.seq !== record.seq || previous.nativeJson.length !== record.nativeJson.length || !previous.nativeJson.every((byte, index) => byte === record.nativeJson[index]))))
      return []
    unique.set(record.id, record)
  }
  const ordered = [...unique.values()].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0)
  const entries: NativeEntry[] = []
  let turnId = ''
  let previousEnd = 0n
  for (const record of ordered) {
    if (record.seq < previousEnd || record.seq + BigInt(record.nativeJson.length) > maxSeq)
      return []
    previousEnd = record.seq + BigInt(record.nativeJson.length) + 1n
    let envelope: unknown
    try {
      envelope = JSON.parse(decoder.decode(record.nativeJson))
    }
    catch {
      envelope = {}
    }
    const object = isObject(envelope) ? envelope : {}
    const payload = pickObject(object, 'payload') ?? {}
    if (object.type === 'session_meta' && payload.id !== sessionId)
      return []
    if (object.type === 'turn_context')
      turnId = pickString(payload, 'turn_id')
    else if (object.type === 'event_msg' && ['task_started', 'turn_started'].includes(pickString(payload, 'type')))
      turnId = pickString(payload, 'turn_id') || turnId
    entries.push({ record, envelope: object, payload, turnId, index: entries.length })
  }
  return entries
}

function contentText(payload: Record<string, unknown>): { text: string, unsupported: boolean, images: { url: string, mimeType: string }[] } {
  const content: unknown[] = Array.isArray(payload.content) ? payload.content : []
  const text: string[] = []
  let unsupported = false
  const images: { url: string, mimeType: string }[] = []
  for (const block of content) {
    if (isObject(block) && ['input_text', 'output_text', 'text'].includes(pickString(block, 'type')) && typeof block.text === 'string') {
      text.push(block.text)
    }
    else if (isObject(block) && ['input_image', 'image'].includes(pickString(block, 'type'))) {
      const url = pickString(block, 'image_url')
      const match = inlineImage.exec(url)
      if (match?.[1])
        images.push({ url, mimeType: match[1] })
      else
        unsupported = true
    }
    else {
      unsupported = true
    }
  }
  return { text: text.join('\n'), unsupported, images }
}

/** Only embedded raster bytes become external user attachment previews. */
export function codexExternalUserAttachments(payload: Record<string, unknown> | undefined): UserMessageAttachment[] | undefined {
  if (payload?.nativeHistory !== true || !Array.isArray(payload.attachments))
    return undefined
  return payload.attachments.flatMap((value): UserMessageAttachment[] => {
    if (!isObject(value))
      return []
    const image = pickObject(value, 'image')
    const url = pickString(image, 'url')
    const match = inlineImage.exec(url)
    const source = match?.[1] ? parseImageBlock({ type: 'inputImage', imageUrl: url }) : null
    return source && match?.[1] ? [{ filename: 'Attached image', mimeType: match[1], image: source }] : []
  })
}

function mirrorText(entry: NativeEntry): { role: string, text: string } | null {
  if (entry.envelope.type !== 'event_msg' || typeof entry.payload.message !== 'string')
    return null
  if (entry.payload.type === 'user_message')
    return { role: 'user', text: entry.payload.message }
  if (entry.payload.type === 'agent_message')
    return { role: 'assistant', text: entry.payload.message }
  return null
}

function mirroredByResponse(entry: NativeEntry, entries: readonly NativeEntry[]): boolean {
  const mirror = mirrorText(entry)
  if (!mirror)
    return false
  return entries.some((candidate) => {
    if (candidate.envelope.type !== 'response_item' || candidate.payload.type !== 'message' || candidate.payload.role !== mirror.role)
      return false
    if (entry.turnId && candidate.turnId && entry.turnId !== candidate.turnId)
      return false
    if ((!entry.turnId || !candidate.turnId) && Math.abs(entry.index - candidate.index) > 3)
      return false
    return contentText(candidate.payload).text === mirror.text
  })
}

function nativeMessage(
  entry: NativeEntry,
  sessionId: string,
  projection: Record<string, unknown> | undefined,
  source: MessageSource,
  blockIndex = 0,
  spanId = '',
  spanType = '',
): AgentChatMessage | null {
  const { record } = entry
  const offset = BigInt(blockIndex)
  if (record.seq <= 0n || offset >= BigInt(record.nativeJson.length) || record.seq + offset > maxSeq)
    return null
  const timestamp = pickString(entry.envelope, 'timestamp')
  const createdAt = timestamp.length <= 128 && Number.isFinite(Date.parse(timestamp)) ? timestamp : ''
  return create(AgentChatMessageSchema, {
    id: `${record.id}:block:${blockIndex}`,
    seq: record.seq + offset,
    source,
    content: record.nativeJson,
    contentCompression: ContentCompression.NONE,
    supplementalContentCompression: ContentCompression.NONE,
    ...(projection
      ? {
          supplementalContent: encoder.encode(JSON.stringify({
            provider: { externalNativeConversation: { format: NATIVE_CONVERSATION_FORMAT.CodexRolloutJSONL, projection } },
          })),
        }
      : {}),
    agentProvider: AgentProvider.CODEX,
    agentSessionId: sessionId,
    transcriptOnly: true,
    createdAt,
    spanId,
    spanType,
    spanColor: -1,
    spanLines: '[]',
  })
}

/** Resolve only an explicitly projected external row; raw JSON remains untouched. */
export function codexExternalHistoryProjection(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const metadata = isObject(parsed.supplementalContent) ? pickObject(parsed.supplementalContent, 'externalNativeConversation') : undefined
  return metadata?.format === NATIVE_CONVERSATION_FORMAT.CodexRolloutJSONL
    ? pickObject(metadata, 'projection') ?? undefined
    : undefined
}

/** Convert saved native records to ephemeral read-only rows, never managed events. */
export function projectCodexNativeConversation(
  records: readonly NativeConversationRecord[],
  format: string,
  sessionId: string,
): AgentChatMessage[] {
  if (format !== NATIVE_CONVERSATION_FORMAT.CodexRolloutJSONL || !sessionId)
    return []
  const entries = parsedEntries(records, sessionId)
  const messages: AgentChatMessage[] = []
  for (const entry of entries) {
    const { envelope, payload } = entry
    const push = (projection?: Record<string, unknown>, source = MessageSource.AGENT, index = 0, spanId = '', spanType = '') => {
      const message = nativeMessage(entry, sessionId, projection, source, index, spanId, spanType)
      if (message)
        messages.push(message)
    }
    // These records describe the store/configuration, not a conversation item.
    if (envelope.type === 'session_meta' || envelope.type === 'turn_context')
      continue
    if (mirroredByResponse(entry, entries))
      continue
    if (envelope.type === 'response_item') {
      const type = pickString(payload, 'type')
      if (type === 'message' && (payload.role === 'user' || payload.role === 'assistant')) {
        const content = contentText(payload)
        const userImages = payload.role === 'user' ? content.images : []
        const displayed = Boolean(content.text) || userImages.length > 0
        if (displayed) {
          push(payload.role === 'user'
            ? { content: content.text, nativeHistory: true, attachments: userImages.map(image => ({ filename: 'Attached image', mime_type: image.mimeType, image: { url: image.url } })) }
            : { item: { type: CODEX_ITEM.AgentMessage, id: pickString(payload, 'id') || entry.record.id, text: content.text } }, payload.role === 'user' ? MessageSource.USER : MessageSource.AGENT)
        }
        if (content.unsupported || (payload.role !== 'user' && content.images.length > 0) || !displayed)
          push(undefined, MessageSource.AGENT, displayed ? 1 : 0)
        continue
      }
      if (type === 'reasoning') {
        const summary: unknown[] = Array.isArray(payload.summary) ? payload.summary : []
        const content: unknown[] = Array.isArray(payload.content) ? payload.content : []
        const text = [...summary, ...content].map(block => typeof block === 'string' ? block : pickString(isObject(block) ? block : undefined, 'text')).filter(Boolean)
        if (text.length) {
          push({ item: { ...payload, type: CODEX_ITEM.Reasoning, summary: text, content: [] } })
          continue
        }
      }
      const callId = pickString(payload, 'call_id')
      if (callId && ['function_call', 'function_call_output', 'custom_tool_call', 'custom_tool_call_output'].includes(type)) {
        // Preserve actual native item and identity. No item/completed event,
        // success, exit code, status, or completion value is manufactured.
        const rawExec = type.startsWith('custom_tool_call')
        const spanType = rawExec ? pickString(payload, 'name') || 'exec' : 'native-function'
        push({ nativeHistory: true, item: payload, threadId: sessionId, turnId: entry.turnId }, MessageSource.AGENT, 0, callId, spanType)
        continue
      }
      // A future native item remains visible as its original JSON.
      push()
      continue
    }
    if (envelope.type === 'event_msg') {
      const mirror = mirrorText(entry)
      if (mirror) {
        push(mirror.role === 'user' ? { content: mirror.text } : { item: { type: CODEX_ITEM.AgentMessage, id: entry.record.id, text: mirror.text } }, mirror.role === 'user' ? MessageSource.USER : MessageSource.AGENT)
        if (Array.isArray(payload.images) && payload.images.length)
          push(undefined, MessageSource.AGENT, 1)
        continue
      }
      if (payload.type === 'agent_reasoning' && typeof payload.text === 'string') {
        push({ item: { type: CODEX_ITEM.Reasoning, text: payload.text } })
        continue
      }
      if (payload.type === 'token_count')
        continue
    }
    push()
  }
  return messages
}
