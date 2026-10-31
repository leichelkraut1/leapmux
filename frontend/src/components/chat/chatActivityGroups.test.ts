import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatRow } from './model/row'
import type { ChatRowExtraction } from './rowExtraction'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { createActivityEntryProjection, createActivityGroupBuilder, isCollapsibleActivity } from './chatActivityGroups'
import { prepareMessage } from './rowPreparation'

function extracted(row: ChatRow): ChatRowExtraction {
  return { kind: 'row', row, completion: null }
}

function entry(id: string, seq: bigint, kind: 'tool_use' | 'tool_result' | 'assistant_thinking' | 'user_content' | 'assistant_text' | 'notification' = 'tool_use', session = 'session'): ClassifiedEntry {
  const message = create(AgentChatMessageSchema, {
    id,
    seq,
    agentSessionId: session,
    source: MessageSource.USER,
    content: new TextEncoder().encode('{"content":"fixture"}'),
  })
  return {
    ...prepareMessage(message),
    category: kind === 'notification' ? { kind, entries: [] } : { kind },
    parsedSpanLines: [],
    freshness: { revisionKey: id, isChildTranscript: false, settingsLabelRevision: '' },
    spanLinesRef: '',
    settingsLabelDependencies: [],
  }
}

const eligible = (value: ClassifiedEntry) => value.category.kind === 'tool_use' || value.category.kind === 'tool_result' || value.category.kind === 'assistant_thinking'

describe('isCollapsibleActivity', () => {
  it('folds ordinary tools and reasoning without interpreting provider payloads', () => {
    expect(isCollapsibleActivity(extracted(toolRow(toolCallFixture('execute'))))).toBe(true)
    expect(isCollapsibleActivity(extracted(toolRow(toolCallFixture('read', { status: 'in_progress' }))))).toBe(true)
    expect(isCollapsibleActivity(extracted({ kind: 'assistant-thinking', text: 'Progress' }))).toBe(true)
  })

  it.each([
    ['read image', toolRow(toolCallFixture('read', { images: [{ filePath: '/project/proof.png' }] }))],
    ['execute image', toolRow(toolCallFixture('execute', { extraContent: [{ type: 'image', source: { filePath: '/project/proof.png' } }] }))],
    ['MCP image', toolRow(toolCallFixture('mcp', { result: { content: [{ type: 'image', source: { filePath: '/project/proof.png' } }] } }))],
  ] as const)('keeps a %s deliverable visible without opening activity', (_label, row) => {
    expect(isCollapsibleActivity(extracted(row))).toBe(false)
  })

  it('keeps questions, decisions, deliverables and every abnormal tool outcome visible', () => {
    for (const kind of ['question', 'switch_mode', 'todo', 'image', 'chart'] as const)
      expect(isCollapsibleActivity(extracted(toolRow(toolCallFixture(kind))))).toBe(false)
    for (const status of ['failed', 'declined', 'cancelled', 'incomplete'] as const)
      expect(isCollapsibleActivity(extracted(toolRow(toolCallFixture('execute', { status }))))).toBe(false)
    expect(isCollapsibleActivity({ kind: 'failed', payload: {}, error: new Error('parse'), completion: null })).toBe(false)
    expect(isCollapsibleActivity({ kind: 'unsupported', payload: {}, completion: null })).toBe(false)
    expect(isCollapsibleActivity({ ...extracted({ kind: 'assistant-thinking', text: 'Interrupted' }), completion: 'interrupted' })).toBe(false)
    expect(isCollapsibleActivity(extracted({ kind: 'assistant-text', text: 'Answer\n```ts\nconst result = 1\n```' }))).toBe(false)
    expect(isCollapsibleActivity(extracted({ kind: 'assistant-plan', text: 'Plan to approve' }))).toBe(false)
  })
})

describe('createActivityGroupBuilder', () => {
  it('groups only consecutive activity and retains user, answer and notification boundaries', () => {
    const build = createActivityGroupBuilder()
    const rows = [entry('u', 1n, 'user_content'), entry('a', 2n), entry('b', 3n, 'assistant_thinking'), entry('answer', 4n, 'assistant_text'), entry('c', 5n, 'tool_result'), entry('notice', 6n, 'notification')]
    const groups = build(rows, eligible)
    expect(groups.groups.map(group => group.members.map(member => member.message.id))).toEqual([['a', 'b'], ['c']])
    expect(groups.groups.every(group => !group.atTail)).toBe(true)
    const project = createActivityEntryProjection()
    expect(project(rows, groups, () => false).map(row => row.message.id)).toEqual(['u', 'a', 'answer', 'c', 'notice'])
    expect(project(rows, groups, () => true).map(row => row.message.id)).toEqual(rows.map(row => row.message.id))
  })

  it('keeps the group identity and expansion through append, prepend and head trimming', () => {
    const build = createActivityGroupBuilder()
    const a = entry('a', 2n)
    const b = entry('b', 3n)
    const c = entry('c', 4n)
    expect(build([a, b], eligible).groups[0]?.id).toBe('a')
    const expanded = new Set(['a'])
    const project = createActivityEntryProjection()
    const appended = build([a, b, c], eligible)
    expect(appended.groups[0]?.atTail).toBe(true)
    expect(project([a, b, c], appended, group => expanded.has(group.id))).toHaveLength(3)
    const earlier = entry('earlier', 1n)
    expect(build([earlier, a, b, c], eligible).groups[0]?.id).toBe('a')
    const trimmed = build([b, c], eligible)
    expect(trimmed.groups[0]?.id).toBe('a')
    expect(project([b, c], trimmed, group => expanded.has(group.id))).toHaveLength(2)
  })

  it('does not let a prepended fragment steal the retained identity after a split', () => {
    const build = createActivityGroupBuilder()
    const a = entry('a', 3n)
    const b = entry('b', 4n)
    const z = entry('z', 1n)
    const x = entry('x', 2n)
    build([a, b], eligible)
    build([z, x, a, b], eligible)
    const split = build([z, x, a, b], value => value.message.id !== 'x')
    expect(split.groups.map(group => group.id)).toEqual(['z', 'a'])
    expect(split.byMemberId.get('a')?.id).toBe('a')
  })

  it('separates sessions and gives split groups distinct identities when an error becomes visible', () => {
    const build = createActivityGroupBuilder()
    const a = entry('a', 1n)
    const b = entry('b', 2n)
    const c = entry('c', 3n)
    build([a, b, c], eligible)
    const split = build([a, b, c], value => value.message.id !== 'b')
    expect(split.groups.map(group => group.id)).toEqual(['a', 'c'])
    expect(build([a, entry('foreign', 2n, 'tool_use', 'other')], eligible).groups).toHaveLength(2)
  })
})

describe('createActivityEntryProjection', () => {
  it('keeps a stable header identity and leaves original records and rails untouched', () => {
    const a = entry('a', 1n)
    const b = entry('b', 2n)
    a.parsedSpanLines = [null]
    const rows = [a, b]
    const groups = createActivityGroupBuilder()(rows, eligible)
    const project = createActivityEntryProjection()
    const closed = project(rows, groups, () => false)
    const open = project(rows, groups, () => true)
    expect(closed[0]).toBe(open[0])
    expect(closed[0]?.message).toBe(a.message)
    expect(closed[0]?.parsedSpanLines).toEqual([])
    expect(a.parsedSpanLines).toEqual([null])
    expect(open[1]).toBe(b)
    expect(rows).toEqual([a, b])
  })
})
