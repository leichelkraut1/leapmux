import type { NativeConversationRecord, NativeConversationRef } from '~/components/chat/nativeConversation'
import type { ReadNativeConversationResponse } from '~/generated/proto/leapmux/v1/worker_pb'
import { runInNewContext } from 'node:vm'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '~/test-support/async'

const mocks = vi.hoisted(() => ({ read: vi.fn(), project: vi.fn() }))
vi.mock('~/api/workerRpc', () => ({ readNativeConversation: (...args: unknown[]) => mocks.read(...args) }))
vi.mock('~/components/chat/providers/registry', async () => ({
  ...await vi.importActual<typeof import('~/components/chat/providers/registry')>('~/components/chat/providers/registry'),
  pluginFor: () => ({ transcript: { projectNativeConversation: mocks.project } }),
}))
const { useNativeAgentTranscript } = await import('./useNativeAgentTranscript')
const disposers: (() => void)[] = []
const ref: NativeConversationRef = { workerId: 'test-pc', workerName: 'Test PC', agentProvider: AgentProvider.CODEX, sessionId: '11111111-2222-3333-4444-555555555555', displayName: 'Generated conversation' }

function record(id: string, seq: bigint, text = id): NativeConversationRecord {
  return { id, seq, nativeJson: new TextEncoder().encode(JSON.stringify({ content: text })) }
}

function page(records = [record('tail', 1000n)], extra: Partial<ReadNativeConversationResponse> = {}): ReadNativeConversationResponse {
  return { $typeName: 'leapmux.v1.ReadNativeConversationResponse', format: 'generated-test-v1', sessionId: ref.sessionId, revision: 'generation-one', displayName: 'Generated title', workingDir: '/generated/work', olderCursor: 'before-1000', newerCursor: 'after-1000', observedAt: '2026-10-28T12:00:00Z', records: records.map(value => ({ $typeName: 'leapmux.v1.NativeConversationRecord', ...value })), hasOlder: true, hasNewer: false, resetRequired: false, partialReason: '', ...extra }
}

function setup() {
  return createRoot((dispose) => {
    disposers.push(dispose)
    const [conversation, setConversation] = createSignal(ref)
    const [online, setOnline] = createSignal(true)
    return { ...useNativeAgentTranscript(conversation, online), setConversation, setOnline, dispose }
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  mocks.read.mockReset()
  mocks.project.mockReset().mockImplementation((records: NativeConversationRecord[], _format: string, sessionId: string) => records.map(item =>
    create(AgentChatMessageSchema, { id: item.id, seq: item.seq, agentProvider: AgentProvider.CODEX, agentSessionId: sessionId, source: MessageSource.AGENT, content: item.nativeJson, contentCompression: ContentCompression.NONE })))
})
afterEach(() => {
  for (const dispose of disposers.splice(0))
    dispose()
  vi.useRealTimers()
})

describe('useNativeAgentTranscript', () => {
  it('accepts genuine native JSON byte views from another realm', async () => {
    const nativeJson = runInNewContext('new Uint8Array([123, 125])') as Uint8Array
    expect(nativeJson).not.toBeInstanceOf(Uint8Array)
    mocks.read.mockResolvedValueOnce(page([{ id: 'cross-realm', seq: 1n, nativeJson }]))
    const view = setup()

    await vi.advanceTimersByTimeAsync(0)

    expect(view.loaded()).toBe(true)
    expect(view.error()).toBe('')
    expect(view.messages().map(message => message.id)).toEqual(['cross-realm'])
    expect(new TextDecoder().decode(view.messages()[0]!.content)).toBe('{}')
    expect(mocks.project).toHaveBeenCalledTimes(1)
  })

  it.each([
    new DataView(new ArrayBuffer(2)),
    new Uint16Array([123, 125]),
    new Uint8ClampedArray([123, 125]),
    { 0: 123, 1: 125, length: 2, [Symbol.toStringTag]: 'Uint8Array' },
  ])('refuses non-byte views and spoofed native JSON byte objects: %s', async (invalid) => {
    mocks.read.mockResolvedValueOnce(page([{ id: 'invalid-bytes', seq: 1n, nativeJson: invalid as unknown as Uint8Array }]))
    const view = setup()

    await vi.advanceTimersByTimeAsync(0)

    expect(view.loaded()).toBe(false)
    expect(view.error()).toBeTruthy()
    expect(view.messages()).toEqual([])
    expect(mocks.project).not.toHaveBeenCalled()
  })

  it('loads full older pages and tail updates without replacing unchanged row identities', async () => {
    mocks.read.mockResolvedValueOnce(page()).mockResolvedValueOnce(page([record('old', 1n)], { hasOlder: false })).mockResolvedValueOnce(page([record('next', 2000n)], { newerCursor: 'after-2000' }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const tail = view.messages()[0]
    await view.loadOlder()
    expect(view.messages().map(message => message.id)).toEqual(['old', 'tail'])
    expect(view.messages()[1]).toBe(tail)
    expect(view.hasOlder()).toBe(false)
    await view.refresh()
    expect(view.messages().map(message => message.id)).toEqual(['old', 'tail', 'next'])
    expect(view.messages()[1]).toBe(tail)
    expect(mocks.read.mock.calls.map(call => call[1].direction)).toEqual(['latest', 'before', 'after'])
    expect(mocks.read.mock.calls[2]![1].cursor).toBe('after-1000')
    expect(view.title()).toBe('Generated title')
  })

  it('merges changed existing records, increments content revision and resolves only loaded context', async () => {
    mocks.read.mockResolvedValueOnce(page()).mockResolvedValueOnce(page([record('tail', 1000n, 'replacement')]))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const before = view.messages()[0]
    const resolver = view.resolver()
    await view.refresh()
    expect(view.messages()).toHaveLength(1)
    expect(view.messages()[0]).not.toBe(before)
    expect(resolver.contentVersion('tail')).toBe(1)
    expect(new TextDecoder().decode(view.messages()[0]!.content)).toContain('replacement')
    await resolver.loadSpan({ agentSessionId: ref.sessionId, spanId: 'missing-context' })
    expect(mocks.read).toHaveBeenCalledTimes(2)
  })

  it('keeps transcript identity and skips projection for metadata-only tail replies', async () => {
    mocks.read.mockResolvedValueOnce(page()).mockResolvedValueOnce(page([], {
      displayName: 'Renamed generated chat',
      workingDir: '/generated/updated',
      observedAt: '2026-10-28T12:00:05Z',
      partialReason: 'incomplete_trailing_record',
      olderCursor: 'unused-tail-older-cursor',
      hasOlder: false,
      newerCursor: 'updated-tail-cursor',
    })).mockResolvedValueOnce(page([record('next', 2000n)], { newerCursor: 'after-2000' }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const rows = view.messages()
    const tail = rows[0]
    const version = view.version()
    expect(mocks.project).toHaveBeenCalledTimes(1)

    await view.refresh()

    expect(view.messages()).toBe(rows)
    expect(view.messages()[0]).toBe(tail)
    expect(view.version()).toBe(version)
    expect(mocks.project).toHaveBeenCalledTimes(1)
    expect(view.resolver().contentVersion('tail')).toBe(0)
    expect(view.title()).toBe('Renamed generated chat')
    expect(view.workingDir()).toBe('/generated/updated')
    expect(view.observedAt()).toBe('2026-10-28T12:00:05Z')
    expect(view.partialReason()).toBe('incomplete_trailing_record')
    expect(view.hasOlder()).toBe(true)
    expect(view.hasNewer()).toBe(false)
    expect(view.busy()).toBe(false)
    expect(view.error()).toBe('')

    await view.refresh()
    expect(mocks.read.mock.calls[2]![1]).toMatchObject({ direction: 'after', cursor: 'updated-tail-cursor' })
    expect(mocks.project).toHaveBeenCalledTimes(2)
    expect(view.messages().map(message => message.id)).toEqual(['tail', 'next'])
    expect(view.messages()[0]).toBe(tail)
    expect(view.version()).toBe(version + 1)
  })

  it.each(['format', 'revision'] as const)('refuses a changed %s in an empty tail reply without hiding loaded messages', async (field) => {
    mocks.read.mockResolvedValueOnce(page()).mockResolvedValueOnce(page([], { [field]: 'changed-source' }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const rows = view.messages()
    const version = view.version()

    await view.refresh()

    expect(view.error()).toBeTruthy()
    expect(view.messages()).toBe(rows)
    expect(view.version()).toBe(version)
    expect(mocks.project).toHaveBeenCalledTimes(1)
    expect(view.observedAt()).toBe('2026-10-28T12:00:00Z')
  })

  it('keeps dated messages/title during pending refresh, failure and computer offline', async () => {
    const pending = deferred<ReadNativeConversationResponse>()
    mocks.read.mockResolvedValueOnce(page()).mockReturnValueOnce(pending.promise)
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const rows = view.messages()
    const refreshing = view.refresh()
    expect(view.busy()).toBe(true)
    expect(view.messages()).toBe(rows)
    expect(view.title()).toBe('Generated title')
    pending.reject(new Error('generated transport failure'))
    await refreshing
    expect(view.messages()).toBe(rows)
    expect(view.error()).toContain('last loaded messages remain')
    expect(view.observedAt()).toBe('2026-10-28T12:00:00Z')
    view.setOnline(false)
    await vi.advanceTimersByTimeAsync(15000)
    expect(mocks.read).toHaveBeenCalledTimes(2)
    expect(view.messages()).toBe(rows)
  })

  it('ignores old session replies and aborts the old read on selection change', async () => {
    const first = deferred<ReadNativeConversationResponse>()
    const other = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
    mocks.read.mockReturnValueOnce(first.promise).mockResolvedValueOnce(page([record('second-session', 1n)], { sessionId: other }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const signal = mocks.read.mock.calls[0]![2].signal as AbortSignal
    view.setConversation({ ...ref, sessionId: other })
    await vi.advanceTimersByTimeAsync(0)
    first.resolve(page())
    await vi.advanceTimersByTimeAsync(0)
    expect(signal.aborted).toBe(true)
    expect(view.messages().map(message => message.id)).toEqual(['second-session'])
  })

  it('resets from a fresh latest page after a generation change without mixing old rows', async () => {
    mocks.read.mockResolvedValueOnce(page()).mockResolvedValueOnce(page([], { resetRequired: true, revision: 'replacement' })).mockResolvedValueOnce(page([record('replacement', 1n)], { revision: 'replacement' }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    await view.refresh()
    expect(view.messages().map(message => message.id)).toEqual(['replacement'])
    expect(view.sourceChanged()).toBe(true)
    expect(mocks.read.mock.calls[2]![1]).toMatchObject({ direction: 'latest', cursor: '' })
  })

  it('refuses a nonadvancing older cursor and tears down reads/timers on close', async () => {
    const never = deferred<ReadNativeConversationResponse>()
    mocks.read.mockResolvedValueOnce(page()).mockResolvedValueOnce(page()).mockReturnValueOnce(never.promise)
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    await view.loadOlder()
    expect(view.error()).toBeTruthy()
    const refreshing = view.refresh()
    await vi.advanceTimersByTimeAsync(0)
    const signal = mocks.read.mock.calls[2]![2].signal as AbortSignal
    view.dispose()
    await refreshing
    expect(signal.aborted).toBe(true)
    const count = mocks.read.mock.calls.length
    await vi.advanceTimersByTimeAsync(30000)
    expect(mocks.read).toHaveBeenCalledTimes(count)
  })
})
