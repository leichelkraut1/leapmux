import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { codexNativeEventItem } from './nativeEventItems'

interface NativeMirrorEntry {
  envelope: Record<string, unknown>
  payload: Record<string, unknown>
  turnId: string
}

/** Exact mirrors need an explicit same-turn owner, not proximity or a reused id. */
function sameTurn(left: NativeMirrorEntry, right: NativeMirrorEntry): boolean {
  return !!left.turnId && left.turnId === right.turnId
}

/** Compare JSON values without renaming argument keys or trusting object-key order. */
function sameValue(left: unknown, right: unknown, depth = 0): boolean {
  if (left === right)
    return true
  if (depth >= 64)
    return false
  if (Array.isArray(left) && Array.isArray(right))
    return left.length === right.length && left.every((value, index) => sameValue(value, right[index], depth + 1))
  if (!isObject(left) || !isObject(right))
    return false
  const keys = Object.keys(left)
  return keys.length === Object.keys(right).length && keys.every(key => Object.hasOwn(right, key) && sameValue(left[key], right[key], depth + 1))
}

function withoutFields(value: Record<string, unknown>, fields: readonly string[]): Record<string, unknown> {
  return Object.fromEntries(Object.entries(value).filter(([key]) => !fields.includes(key)))
}

function reasoningParts(item: Record<string, unknown>, core: boolean): unknown[] | undefined {
  const textList = (value: unknown): string[] | undefined => {
    if (value === undefined || value === null)
      return []
    if (!Array.isArray(value))
      return undefined
    const result: string[] = []
    for (const block of value) {
      if (typeof block === 'string') {
        result.push(block)
      }
      else if (!core && isObject(block) && ['summary_text', 'reasoning_text', 'text'].includes(pickString(block, 'type'))
        && typeof block.text === 'string' && Object.keys(block).every(key => key === 'type' || key === 'text')) {
        result.push(block.text)
      }
      else {
        return undefined
      }
    }
    return result
  }
  const summary = textList(core ? item.summary_text : item.summary)
  const content = textList(core ? item.raw_content : item.content)
  return summary && content ? [summary, content] : undefined
}

function coreItem(entry: NativeMirrorEntry, eventType: string): Record<string, unknown> | undefined {
  return entry.envelope.type === 'event_msg' && entry.payload.type === eventType
    ? pickObject(entry.payload, 'item') ?? undefined
    : undefined
}

function mirroredReasoning(entry: NativeMirrorEntry, entries: readonly NativeMirrorEntry[]): boolean {
  const item = coreItem(entry, 'item_started') ?? coreItem(entry, 'item_completed')
  if (item?.type !== 'Reasoning' || !pickString(item, 'id')
    || Object.keys(item).some(key => !['type', 'id', 'summary_text', 'raw_content'].includes(key))) {
    return false
  }
  const parts = reasoningParts(item, true)
  return parts !== undefined && entries.some(candidate => sameTurn(entry, candidate)
    && candidate.envelope.type === 'response_item' && candidate.payload.type === 'reasoning'
    && candidate.payload.id === item.id && sameValue(parts, reasoningParts(candidate.payload, false)))
}

/** An empty started snapshot is superseded only by the same call and exact input. */
function supersededToolStart(entry: NativeMirrorEntry, entries: readonly NativeMirrorEntry[]): boolean {
  const item = coreItem(entry, 'item_started')
  if (!item || !codexNativeEventItem(entry.payload))
    return false
  const lifecycle = item.type === 'DynamicToolCall'
    ? ['status', 'content_items', 'success', 'error', 'duration']
    : item.type === 'CommandExecution'
      ? ['status', 'aggregated_output', 'exit_code', 'duration']
      : item.type === 'McpToolCall' ? ['status', 'result', 'error', 'duration'] : undefined
  if (!lifecycle || lifecycle.some(field => field !== 'status' && item[field] !== undefined && item[field] !== null))
    return false
  return entries.some((candidate) => {
    const completed = coreItem(candidate, 'item_completed')
    return sameTurn(entry, candidate) && codexNativeEventItem(candidate.payload) !== undefined && completed?.type === item.type && completed.id === item.id
      && sameValue(withoutFields(item, lifecycle), withoutFields(completed, lifecycle))
  })
}

function argumentsValue(value: unknown): unknown {
  if (typeof value !== 'string')
    return value
  try {
    const parsed: unknown = JSON.parse(value)
    return parsed
  }
  catch {
    return undefined
  }
}

/** Only fully understood content blocks can prove a raw result is a mirror. */
function outputParts(value: unknown): unknown[] | undefined {
  if (typeof value === 'string')
    return [{ type: 'text', text: value }]
  if (!Array.isArray(value))
    return undefined
  const output: unknown[] = []
  for (const block of value) {
    if (!isObject(block))
      return undefined
    if (['inputText', 'input_text', 'output_text', 'text'].includes(pickString(block, 'type'))
      && typeof block.text === 'string' && Object.keys(block).every(key => key === 'type' || key === 'text')) {
      output.push({ type: 'text', text: block.text })
    }
    else {
      // Images, structured results and future blocks keep their original rows.
      // Matching just the text beside them would silently discard real content.
      return undefined
    }
  }
  return output
}

/**
 * A native completed dynamic item already carries both call and result. Prefer
 * it to a proved raw request/result pair: unlike a response-item request's own
 * completed word, the core item's status states the actual tool outcome.
 */
function mirroredFunctionPair(entry: NativeMirrorEntry, entries: readonly NativeMirrorEntry[]): boolean {
  if (entry.envelope.type !== 'response_item' || !['function_call', 'function_call_output'].includes(pickString(entry.payload, 'type')))
    return false
  const id = pickString(entry.payload, 'call_id')
  if (!id || !entry.turnId)
    return false
  const requests = entries.filter(candidate => sameTurn(entry, candidate) && candidate.envelope.type === 'response_item' && candidate.payload.type === 'function_call' && candidate.payload.call_id === id)
  const results = entries.filter(candidate => sameTurn(entry, candidate) && candidate.envelope.type === 'response_item' && candidate.payload.type === 'function_call_output' && candidate.payload.call_id === id)
  if (requests.length !== 1 || results.length !== 1)
    return false
  const request = requests[0]!.payload
  const result = results[0]!.payload
  if (Object.keys(request).some(key => !['type', 'id', 'call_id', 'name', 'namespace', 'arguments', 'status'].includes(key))
    || Object.keys(result).some(key => !['type', 'id', 'call_id', 'output', 'is_error', 'status'].includes(key))) {
    return false
  }
  const args = argumentsValue(request.arguments)
  const output = outputParts(result.output)
  if (args === undefined || output === undefined)
    return false
  return entries.some((candidate) => {
    const item = coreItem(candidate, 'item_completed')
    return sameTurn(entry, candidate) && codexNativeEventItem(candidate.payload) !== undefined && item?.type === 'DynamicToolCall' && item.id === id
      && ['completed', 'failed'].includes(pickString(item, 'status'))
      && pickString(item, 'tool') === pickString(request, 'name')
      && pickString(item, 'namespace') === pickString(request, 'namespace')
      && (typeof result.is_error !== 'boolean' || result.is_error === (item.status === 'failed'))
      && (result.status === undefined || result.status === item.status)
      && sameValue(item.arguments, args) && sameValue(outputParts(item.content_items), output)
  })
}

/** Never suppress a row by type or id alone: every supported mirror proves content and ownership. */
export function codexNativeRecordMirrored(entry: NativeMirrorEntry, entries: readonly NativeMirrorEntry[]): boolean {
  return mirroredReasoning(entry, entries) || supersededToolStart(entry, entries) || mirroredFunctionPair(entry, entries)
}
