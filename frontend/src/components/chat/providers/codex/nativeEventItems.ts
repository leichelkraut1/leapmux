import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Rollouts persist core TurnItem values, not app-server ThreadItem values.
 * Core uses PascalCase variants and several snake_case fields. Normalize only
 * the variants/fields we actually read; never recursively rename tool arguments
 * or results. The original native JSON stays on the projected message.
 *
 * https://github.com/openai/codex/blob/main/codex-rs/protocol/src/items.rs
 */
export function codexNativeEventItem(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  if (payload.type !== 'item_started' && payload.type !== 'item_completed')
    return undefined
  const item = pickObject(payload, 'item')
  if (!item || !pickString(item, 'id'))
    return undefined
  switch (item.type) {
    case 'AgentMessage': {
      if (!Array.isArray(item.content) || item.content.some(block => !isObject(block) || block.type !== 'Text' || typeof block.text !== 'string'))
        return undefined
      return { ...item, type: CODEX_ITEM.AgentMessage, text: item.content.map(block => pickString(isObject(block) ? block : undefined, 'text')).join('\n') }
    }
    case 'Reasoning':
      if (!Array.isArray(item.summary_text) || item.summary_text.some(text => typeof text !== 'string')
        || (item.raw_content !== undefined && (!Array.isArray(item.raw_content) || item.raw_content.some(text => typeof text !== 'string'))))
        return undefined
      return { ...item, type: CODEX_ITEM.Reasoning, summary: item.summary_text, content: item.raw_content ?? [] }
    case 'Plan':
      return typeof item.text === 'string' ? { ...item, type: CODEX_ITEM.Plan } : undefined
    case 'CommandExecution': {
      if (!Array.isArray(item.command) || item.command.some(word => typeof word !== 'string')
        || !['in_progress', 'completed', 'failed', 'declined'].includes(pickString(item, 'status'))
        || (item.aggregated_output !== undefined && item.aggregated_output !== null && typeof item.aggregated_output !== 'string'))
        return undefined
      const argv: string[] = item.command.filter((word): word is string => typeof word === 'string')
      const durationMs = nativeDurationMs(item.duration)
      return {
        ...item,
        type: CODEX_ITEM.CommandExecution,
        command: argv.map(quoteCommandWord).join(' '),
        status: item.status === 'in_progress' ? 'inProgress' : item.status,
        ...(item.aggregated_output === undefined ? {} : { aggregatedOutput: item.aggregated_output }),
        ...(item.exit_code === undefined ? {} : { exitCode: item.exit_code }),
        ...(item.process_id === undefined ? {} : { processId: item.process_id }),
        ...(durationMs === undefined ? {} : { durationMs }),
      }
    }
    case 'McpToolCall':
      return typeof item.server === 'string' && typeof item.tool === 'string' && ['inProgress', 'completed', 'failed'].includes(pickString(item, 'status'))
        ? { ...item, type: CODEX_ITEM.McpToolCall }
        : undefined
    case 'DynamicToolCall': {
      if (typeof item.tool !== 'string' || !['in_progress', 'completed', 'failed'].includes(pickString(item, 'status')))
        return undefined
      const durationMs = nativeDurationMs(item.duration)
      return {
        ...item,
        type: CODEX_ITEM.DynamicToolCall,
        status: item.status === 'in_progress' ? 'inProgress' : item.status,
        ...(item.content_items === undefined ? {} : { contentItems: item.content_items }),
        ...(durationMs === undefined ? {} : { durationMs }),
      }
    }
    case 'HookPrompt':
      return { ...item, type: CODEX_ITEM.HookPrompt }
    case 'ImageView':
      return { ...item, type: CODEX_ITEM.ImageView }
    default:
      return undefined
  }
}

function nativeDurationMs(value: unknown): number | undefined {
  if (!isObject(value) || typeof value.secs !== 'number' || typeof value.nanos !== 'number'
    || !Number.isSafeInteger(value.secs) || value.secs < 0 || !Number.isInteger(value.nanos) || value.nanos < 0 || value.nanos >= 1e9)
    return undefined
  const milliseconds = value.secs * 1000 + value.nanos / 1e6
  return Number.isFinite(milliseconds) ? milliseconds : undefined
}

/** Display argv accurately as shell words; this string is never executed. */
function quoteCommandWord(word: string): string {
  return /^[\w@%+=:,./-]+$/.test(word) ? word : `'${word.replaceAll("'", "'\\''")}'`
}
