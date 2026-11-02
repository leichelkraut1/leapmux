import { pickString } from '~/lib/jsonPick'

/**
 * Claude's saved session store mixes conversation frames with UI/state records.
 * These named records maintain the store; they are not messages authored in the
 * chat. Keep their original bytes in the projected history for diagnostics, but
 * do not give each snapshot, title update or queue acknowledgement a chat row.
 *
 * Unknown types, attachment records and progress content deliberately fall
 * through. A newer native message must keep its raw fallback until we read it.
 */
const STORAGE_RECORD_TYPES = new Set([
  'file-history-snapshot',
  'file-history-delta',
  'custom-title',
  'ai-title',
  'tag',
  'agent-name',
  'agent-color',
  'agent-setting',
  'agent_metadata',
  'pr-link',
  'permission-mode',
  'worktree-state',
  'cost-state',
])

/** Identify only known bookkeeping, without hiding an unexpected message body. */
export function claudeNativeMetadata(payload: Record<string, unknown>): boolean {
  // A record carrying message/content blocks is not merely a storage update,
  // even if a future CLI reuses one of the metadata names above.
  if (payload.message !== undefined || payload.content !== undefined || payload.text !== undefined || payload.error !== undefined)
    return false
  const type = pickString(payload, 'type')
  if (STORAGE_RECORD_TYPES.has(type))
    return true
  // Enqueues may contain a pending prompt under content; the guard above keeps
  // that visible. A dequeue/remove acknowledgement has no conversation body.
  return type === 'queue-operation' && ['enqueue', 'dequeue', 'remove'].includes(pickString(payload, 'operation'))
}
