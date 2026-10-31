import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatRowExtraction } from './rowExtraction'
import { imagesForRow } from './results/rowImages'

/** Decisions use the extracted neutral model, never a provider's raw bytes. */
export function isCollapsibleActivity(extraction: ChatRowExtraction): boolean {
  if (extraction.kind !== 'row' || extraction.completion === 'error' || extraction.completion === 'interrupted')
    return false
  const row = extraction.row
  if (row.kind === 'assistant-thinking')
    return true
  if (row.kind !== 'tool' || row.call.degradation || imagesForRow(row).length > 0)
    return false
  if (row.call.status === 'failed' || row.call.status === 'declined' || row.call.status === 'cancelled' || row.call.status === 'incomplete')
    return false
  // Questions, mode/plan decisions and rendered deliverables remain readable
  // without opening an activity group. Pending controls also live outside ChatView.
  return row.call.kind !== 'question' && row.call.kind !== 'switch_mode'
    && row.call.kind !== 'todo' && row.call.kind !== 'image' && row.call.kind !== 'chart'
}

export interface ChatActivityGroup {
  /** Retained while any member survives an append, prepend or window trim. */
  id: string
  head: ClassifiedEntry
  members: readonly ClassifiedEntry[]
  /** Only the final group in the loaded transcript can describe live work. */
  atTail: boolean
}

export interface ChatActivityGroups {
  groups: readonly ChatActivityGroup[]
  byMemberId: ReadonlyMap<string, ChatActivityGroup>
  byHeadId: ReadonlyMap<string, ChatActivityGroup>
}

/**
 * Remember identities only for the current window. The bounded message UI store
 * separately retains explicit expansion choices. A continuing group keeps its
 * identity even when pagination prepends an earlier member or trims its head.
 */
export function createActivityGroupBuilder() {
  let previous = new Map<string, string>()
  return (entries: readonly ClassifiedEntry[], collapsible: (entry: ClassifiedEntry) => boolean): ChatActivityGroups => {
    const groups: ChatActivityGroup[] = []
    const byMemberId = new Map<string, ChatActivityGroup>()
    const byHeadId = new Map<string, ChatActivityGroup>()
    const next = new Map<string, string>()
    const claimed = new Set<string>()
    const partitions: ClassifiedEntry[][] = []
    let members: ClassifiedEntry[] = []
    const flush = () => {
      const head = members[0]
      if (!head)
        return
      partitions.push(members)
      members = []
    }
    for (const entry of entries) {
      const prior = members.at(-1)
      // ChatView owns the agent; the message carries its provider session.
      if (prior && prior.message.agentSessionId !== entry.message.agentSessionId)
        flush()
      if (collapsible(entry))
        members.push(entry)
      else
        flush()
    }
    flush()
    // Reserve an existing identity for the partition that still contains its
    // original member. A prepended fragment must not steal it after a split.
    const reserved = new Map<string, number>()
    partitions.forEach((partition, index) => {
      for (const entry of partition)
        reserved.set(entry.message.id, index)
    })
    partitions.forEach((partition, index) => {
      const head = partition[0]!
      const retained = partition.map(entry => previous.get(entry.message.id)).find(id => id !== undefined
        && !claimed.has(id) && (!reserved.has(id) || reserved.get(id) === index))
      const id = retained ?? head.message.id
      claimed.add(id)
      const group = { id, head, members: partition, atTail: partition.at(-1) === entries.at(-1) }
      groups.push(group)
      byHeadId.set(head.message.id, group)
      for (const entry of partition) {
        next.set(entry.message.id, id)
        byMemberId.set(entry.message.id, group)
      }
    })
    previous = next
    return { groups, byMemberId, byHeadId }
  }
}

/**
 * Only the head occupies a virtual slot while collapsed. Expanded members retain
 * their original IDs/sequence numbers and individual virtualized rows. The head's
 * view-only copy has no span rails: the summary is not itself a tool span.
 */
export function createActivityEntryProjection() {
  const heads = new WeakMap<ClassifiedEntry, ClassifiedEntry>()
  return (entries: readonly ClassifiedEntry[], groups: ChatActivityGroups, expanded: (group: ChatActivityGroup) => boolean): ClassifiedEntry[] => {
    const result: ClassifiedEntry[] = []
    for (const entry of entries) {
      const group = groups.byMemberId.get(entry.message.id)
      if (!group) {
        result.push(entry)
      }
      else if (group.head === entry) {
        let head = heads.get(entry)
        if (!head) {
          head = { ...entry, parsedSpanLines: [] }
          heads.set(entry, head)
        }
        result.push(head)
      }
      else if (expanded(group)) {
        result.push(entry)
      }
    }
    return result
  }
}
