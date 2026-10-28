import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ToolCall } from '../../../model/toolCall'
import type { ToolSpanContext } from '../../../rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { extractItem } from './item'

export type NativeHistoryFunctionRole = 'request' | 'result'

/** Saved function result records are evidence; missing output never becomes completion. */
export function codexNativeHistoryFunctionRole(payload: Record<string, unknown> | undefined): NativeHistoryFunctionRole | null {
  if (payload?.nativeHistory !== true)
    return null
  const item = extractItem(payload)
  if (!item || !pickString(item, 'call_id'))
    return null
  return item.type === 'function_call' ? 'request' : item.type === 'function_call_output' ? 'result' : null
}

function counterpart(payload: Record<string, unknown>, sessionId: string | undefined, parsed: ParsedMessageContent | undefined, role: NativeHistoryFunctionRole): Record<string, unknown> | undefined {
  const other = parsed?.parentObject
  if (sessionId && parsed?.agentSessionId && sessionId !== parsed.agentSessionId)
    return undefined
  if (!other || codexNativeHistoryFunctionRole(other) !== role || extractItem(other)?.call_id !== extractItem(payload)?.call_id)
    return undefined
  for (const field of ['threadId', 'turnId']) {
    const own = pickString(payload, field)
    const candidate = pickString(other, field)
    if (own && candidate && own !== candidate)
      return undefined
  }
  return extractItem(other) ?? undefined
}

function argumentsObject(value: unknown): Record<string, unknown> {
  if (isObject(value))
    return value
  if (typeof value === 'string') {
    try {
      const parsed: unknown = JSON.parse(value)
      if (isObject(parsed))
        return parsed
    }
    catch {}
    return { nativeArguments: value }
  }
  return {}
}

const inlineImage = /^data:image\/(?:png|jpeg|gif|webp);base64,[A-Za-z0-9+/]+={0,2}$/

function outputContent(value: unknown): McpContentItem[] {
  if (typeof value === 'string')
    return [{ type: 'text', text: value }]
  const values: unknown[] = Array.isArray(value) ? value : [value]
  return values.map((block): McpContentItem => {
    if (isObject(block)) {
      if (['input_text', 'output_text', 'text'].includes(pickString(block, 'type')) && typeof block.text === 'string')
        return { type: 'text', text: block.text }
      const url = pickString(block, 'image_url') || pickString(block, 'imageUrl')
      if (['input_image', 'inputImage', 'image'].includes(pickString(block, 'type')) && inlineImage.test(url))
        return { type: 'image', source: { url } }
    }
    return { type: 'unknown', raw: block }
  })
}

/** Pure native function-call rendering, separate from all provider runtime sinks. */
export function codexNativeHistoryFunctionCall(parsed: ParsedMessageContent, sides: ToolSpanContext): { call: ToolCall<'mcp'>, role: NativeHistoryFunctionRole } | null {
  const payload = parsed.parentObject
  const role = codexNativeHistoryFunctionRole(payload)
  const own = extractItem(payload)
  if (!payload || !own || !role)
    return null
  const request = role === 'request' ? own : counterpart(payload, parsed.agentSessionId, sides.request, 'request')
  const result = role === 'result' ? own : counterpart(payload, parsed.agentSessionId, sides.result, 'result')
  const name = pickString(request, 'name') || 'Function output'
  const error = result?.is_error === true || pickObject(result, 'output')?.is_error === true
  const call = createToolCall({
    id: pickString(own, 'call_id'),
    name,
    lifecycle: { frameStatus: 'unstated', providerOutcome: error ? 'failed' : null, retainedOutcome: null, rowFinal: role === 'result', resultFrameLanded: result !== undefined },
  }, {
    kind: 'mcp',
    request: { server: '', tool: name, args: argumentsObject(request?.arguments) },
    ...(result ? { result: { content: outputContent(result.output) } } : {}),
  })
  return call.kind === 'mcp' ? { call, role } : null
}
