import type { MessageInitShape } from '@bufbuild/protobuf'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import type { InspectNativeSessionControlResponse } from '~/generated/proto/leapmux/v1/worker_pb'
import { create } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { InspectNativeSessionControlResponseSchema, NativeSessionInputBehavior, NativeSessionInputDisposition, NativeSessionInputOutcomeSchema, NativeSessionInputState } from '~/generated/proto/leapmux/v1/worker_pb'
import { ChannelError } from '~/lib/channelError'
import { deferred } from '~/test-support/async'

const mocks = vi.hoisted(() => ({ inspect: vi.fn(), send: vi.fn(), interrupt: vi.fn() }))
vi.mock('~/api/workerRpc', () => ({
  inspectNativeSessionControl: (...args: unknown[]) => mocks.inspect(...args),
  sendNativeSessionInput: (...args: unknown[]) => mocks.send(...args),
  interruptNativeSession: (...args: unknown[]) => mocks.interrupt(...args),
}))
const { useNativeAgentControl } = await import('./useNativeAgentControl')
const ref: NativeConversationRef = { workerId: 'generated-computer', workerName: 'Generated PC', agentProvider: AgentProvider.CODEX, sessionId: '11111111-2222-3333-4444-555555555555' }
const disposers: (() => void)[] = []

function capability(extra: MessageInitShape<typeof InspectNativeSessionControlResponseSchema> = {}) {
  return create(InspectNativeSessionControlResponseSchema, {
    sessionId: ref.sessionId,
    ownerLabel: 'Generated CLI',
    transportLabel: 'Generated native bridge',
    canSend: true,
    inputBehavior: NativeSessionInputBehavior.QUEUE_NEXT_TURN,
    bindingToken: 'generated-binding',
    observedAt: new Date().toISOString(),
    expiresAt: new Date(Date.now() + 20_000).toISOString(),
    activityState: AgentActivityState.IDLE,
    ...extra,
  })
}

function setup() {
  return createRoot((dispose) => {
    disposers.push(dispose)
    const [conversation, setConversation] = createSignal(ref)
    const [online, setOnline] = createSignal(true)
    return { ...useNativeAgentControl(conversation, online), setConversation, setOnline, dispose }
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-30T14:00:00Z'))
  mocks.inspect.mockReset().mockImplementation((_worker, request) => Promise.resolve(capability({ sessionId: request.sessionId })))
  mocks.send.mockReset().mockImplementation((_worker, request) => Promise.resolve({ sessionId: request.sessionId, requestId: request.requestId, disposition: NativeSessionInputDisposition.QUEUED }))
  mocks.interrupt.mockReset().mockImplementation((_worker, request) => Promise.resolve({ sessionId: request.sessionId, requestId: request.requestId }))
})
afterEach(() => {
  for (const dispose of disposers.splice(0))
    dispose()
  vi.useRealTimers()
})

describe('useNativeAgentControl', () => {
  it('keeps unsupported sessions read only and displays their provider reason', async () => {
    mocks.inspect.mockResolvedValue(capability({ canSend: false, bindingToken: '', expiresAt: '', unavailableReason: 'Enable the generated bridge in the original app.' }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(view.canSend()).toBe(false)
    expect(view.hasComposer()).toBe(false)
    expect(view.unavailableReason()).toBe('Enable the generated bridge in the original app.')
    expect(await view.send('generated text')).toBe(false)
    expect(mocks.send).not.toHaveBeenCalled()
    expect(mocks.interrupt).not.toHaveBeenCalled()
  })

  it('queues exact text on the bound original session and waits for acknowledgement', async () => {
    const gate = deferred<{ sessionId: string, requestId: string, disposition: NativeSessionInputDisposition }>()
    mocks.send.mockReturnValueOnce(gate.promise)
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const sending = view.send('  harmless generated text\nsecond line  ')
    const request = mocks.send.mock.calls[0]![1]
    expect(mocks.send).toHaveBeenCalledWith(ref.workerId, { agentProvider: ref.agentProvider, sessionId: ref.sessionId, bindingToken: 'generated-binding', requestId: expect.any(String), inputBehavior: NativeSessionInputBehavior.QUEUE_NEXT_TURN, text: '  harmless generated text\nsecond line  ' })
    expect(view.pending()).toBe('send')
    expect(view.receipt()).toBe('')
    expect(await view.send('another message')).toBe(false)
    gate.resolve({ sessionId: ref.sessionId, requestId: request.requestId, disposition: NativeSessionInputDisposition.QUEUED })
    expect(await sending).toBe(true)
    expect(view.pending()).toBeUndefined()
    expect(view.receipt()).toBe('Message queued in the original app for its next turn.')
  })

  it('distinguishes accepted input from a queued input receipt', async () => {
    mocks.send.mockImplementationOnce((_worker, request) => Promise.resolve({ sessionId: request.sessionId, requestId: request.requestId, disposition: NativeSessionInputDisposition.ACCEPTED }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated accepted text')).toBe(true)
    expect(view.receipt()).toBe('Message accepted by the original app.')
  })

  it('requires an advertised input behavior and sends native-submit consent explicitly', async () => {
    mocks.inspect.mockResolvedValueOnce(capability({ inputBehavior: NativeSessionInputBehavior.UNSPECIFIED }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(view.canSend()).toBe(false)
    expect(view.hasComposer()).toBe(false)
    expect(view.unavailableReason()).toMatch(/supported input behavior/)
    expect(await view.send('generated text')).toBe(false)
    mocks.inspect.mockResolvedValueOnce(capability({ inputBehavior: NativeSessionInputBehavior.SEND_NOW }))
    await view.refresh()
    mocks.send.mockImplementationOnce((_worker, request) => Promise.resolve({ sessionId: request.sessionId, requestId: request.requestId, disposition: NativeSessionInputDisposition.ACCEPTED }))
    expect(await view.send('generated immediate input')).toBe(true)
    expect(mocks.send.mock.calls[0]![1].inputBehavior).toBe(NativeSessionInputBehavior.SEND_NOW)
    expect(view.receipt()).not.toMatch(/queue|next turn/)
  })

  it('never retries a write automatically and retains its id for an explicit identical retry', async () => {
    mocks.send.mockRejectedValueOnce(new Error('generated lost acknowledgement'))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated retained text')).toBe(false)
    const first = mocks.send.mock.calls[0]![1]
    expect(view.error()).toMatch(/did not confirm delivery.*text is kept/)
    mocks.inspect.mockResolvedValue(capability({ bindingToken: 'generated-renewed-binding', inputBehavior: NativeSessionInputBehavior.SEND_NOW }))
    await vi.advanceTimersByTimeAsync(5000)
    expect(mocks.send).toHaveBeenCalledOnce()
    expect(await view.send('generated retained text')).toBe(true)
    expect(mocks.send.mock.calls[1]![1].requestId).toBe(first.requestId)
    expect(mocks.send.mock.calls[1]![1].bindingToken).toBe(first.bindingToken)
    expect(mocks.send.mock.calls[1]![1].inputBehavior).toBe(first.inputBehavior)
  })

  it.each([Code.FailedPrecondition, Code.InvalidArgument, Code.PermissionDenied])('allows a fresh explicit attempt after a known pre-delivery refusal %s', async (code) => {
    mocks.send.mockRejectedValueOnce(new ChannelError('rpc', 'generated request was refused before delivery', { code }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated unchanged text')).toBe(false)
    const first = mocks.send.mock.calls[0]![1]
    expect(view.error()).toMatch(/Message was not sent/)
    expect(view.error()).not.toMatch(/unconfirmed|did not confirm/)
    mocks.inspect.mockResolvedValueOnce(capability({ bindingToken: 'generated-fresh-binding' }))
    await view.refresh()
    expect(mocks.send).toHaveBeenCalledOnce()
    expect(await view.send('generated unchanged text')).toBe(true)
    const second = mocks.send.mock.calls[1]![1]
    expect(second.requestId).not.toBe(first.requestId)
    expect(second.bindingToken).toBe('generated-fresh-binding')
    expect(second.text).toBe(first.text)
    expect(mocks.send).toHaveBeenCalledTimes(2)
  })

  it.each([Code.Aborted, Code.DeadlineExceeded, Code.Unavailable, Code.Unknown, Code.Internal])('retains the original attempt after uncertain RPC failure %s', async (code) => {
    mocks.send.mockRejectedValueOnce(new ChannelError('rpc', 'generated uncertain delivery', { code }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated uncertain text')).toBe(false)
    const first = mocks.send.mock.calls[0]![1]
    mocks.inspect.mockResolvedValueOnce(capability({ bindingToken: 'generated-renewed-binding' }))
    await view.refresh()
    expect(mocks.send).toHaveBeenCalledOnce()
    expect(await view.send('generated uncertain text')).toBe(true)
    const second = mocks.send.mock.calls[1]![1]
    expect(second.requestId).toBe(first.requestId)
    expect(second.bindingToken).toBe(first.bindingToken)
    expect(second.text).toBe(first.text)
  })

  it('does not treat a stale retry as proof that the original uncertain input was never delivered', async () => {
    mocks.send
      .mockRejectedValueOnce(new ChannelError('rpc', 'generated lost original acknowledgement', { code: Code.Aborted }))
      .mockRejectedValueOnce(new ChannelError('rpc', 'generated old binding refused after reconnect', { code: Code.FailedPrecondition }))
      .mockRejectedValueOnce(new ChannelError('rpc', 'generated old binding still refused', { code: Code.FailedPrecondition }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated original uncertain input')).toBe(false)
    const first = mocks.send.mock.calls[0]![1]
    mocks.inspect.mockResolvedValueOnce(capability({ bindingToken: 'generated-new-owner-binding' }))
    await view.refresh()
    expect(await view.send(first.text)).toBe(false)
    expect(view.error()).toMatch(/did not confirm delivery/)
    expect(await view.send(first.text)).toBe(false)
    expect(mocks.send.mock.calls.map(call => call[1].requestId)).toEqual([first.requestId, first.requestId, first.requestId])
    expect(mocks.send.mock.calls.map(call => call[1].bindingToken)).toEqual([first.bindingToken, first.bindingToken, first.bindingToken])
    expect(mocks.send.mock.calls[0]![1]).not.toHaveProperty('uncertain')
  })

  it('enforces the UTF-8 byte limit without dropping or sending oversized text', async () => {
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('é'.repeat(32_769))).toBe(false)
    expect(view.error()).toMatch(/byte limit/)
    expect(mocks.send).not.toHaveBeenCalled()
    expect(await view.send('generated\0text')).toBe(false)
    expect(view.error()).toMatch(/Remove NUL characters/)
    expect(mocks.send).not.toHaveBeenCalled()
    expect(await view.send('é'.repeat(32_768))).toBe(true)
    expect(mocks.send).toHaveBeenCalledOnce()
  })

  it('rejects mismatched delivery acknowledgements', async () => {
    mocks.send.mockResolvedValueOnce({ sessionId: 'different-session', requestId: 'different-request', disposition: NativeSessionInputDisposition.ACCEPTED })
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated text')).toBe(false)
    expect(view.receipt()).toBe('')
    expect(view.error()).toMatch(/did not confirm delivery/)
  })

  it('follows only its own queued input outcomes without resending a dropped message', async () => {
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    await view.send('generated recoverable message')
    let requestId = mocks.send.mock.calls[0]![1].requestId
    const observe = async (state: NativeSessionInputState) => {
      mocks.inspect.mockResolvedValueOnce(capability({ recentInputs: [
        create(NativeSessionInputOutcomeSchema, { requestId: 'another-viewer-request', state: NativeSessionInputState.REJECTED, detail: 'unrelated generated outcome' }),
        create(NativeSessionInputOutcomeSchema, { requestId, state }),
      ] }))
      await view.refresh()
    }
    await observe(NativeSessionInputState.QUEUED)
    expect(view.receipt()).toMatch(/turn has not started/)
    expect(view.recoverableText()).toBe('')
    await observe(NativeSessionInputState.DISPATCHED)
    expect(view.receipt()).toMatch(/handed to the original app/)
    await observe(NativeSessionInputState.DROPPED)
    expect(view.receipt()).toMatch(/dropped.*not resent/)
    expect(view.receipt()).not.toContain('unrelated generated outcome')
    expect(view.recoverableText()).toBe('generated recoverable message')
    await vi.advanceTimersByTimeAsync(5000)
    expect(mocks.send).toHaveBeenCalledOnce()
    await view.send('generated deliberate next message')
    requestId = mocks.send.mock.calls[1]![1].requestId
    await observe(NativeSessionInputState.STARTED)
    expect(view.receipt()).toBe('Message started a turn in the original app.')
    expect(view.recoverableText()).toBe('')
  })

  it('accepts an exact observed start as confirmation when the original RPC acknowledgement is lost', async () => {
    const acknowledgement = deferred<never>()
    mocks.send.mockReturnValueOnce(acknowledgement.promise)
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const sending = view.send('generated observed input')
    const requestId = mocks.send.mock.calls[0]![1].requestId
    mocks.inspect.mockResolvedValueOnce(capability({ recentInputs: [create(NativeSessionInputOutcomeSchema, { requestId, state: NativeSessionInputState.STARTED })] }))
    await view.refresh()
    acknowledgement.reject(new Error('generated lost RPC acknowledgement'))
    expect(await sending).toBe(true)
    expect(view.receipt()).toBe('Message started a turn in the original app.')
    expect(view.error()).toBe('')
    expect(mocks.send).toHaveBeenCalledOnce()
  })

  it('allows a new explicit send after an uncertain input is definitively dropped', async () => {
    mocks.send.mockRejectedValueOnce(new Error('generated lost acknowledgement'))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(await view.send('generated dropped input')).toBe(false)
    const oldId = mocks.send.mock.calls[0]![1].requestId
    mocks.inspect.mockResolvedValueOnce(capability({ recentInputs: [create(NativeSessionInputOutcomeSchema, { requestId: oldId, state: NativeSessionInputState.DROPPED })] }))
    await view.refresh()
    expect(mocks.send).toHaveBeenCalledOnce()
    expect(view.recoverableText()).toBe('generated dropped input')
    expect(await view.send('generated dropped input')).toBe(true)
    expect(mocks.send.mock.calls[1]![1].requestId).not.toBe(oldId)
    expect(mocks.send).toHaveBeenCalledTimes(2)
  })

  it('expires capabilities even when the next inspection cannot finish', async () => {
    mocks.inspect.mockResolvedValueOnce(capability({ expiresAt: new Date(Date.now() + 1000).toISOString() }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(view.canSend()).toBe(true)
    await vi.advanceTimersByTimeAsync(1001)
    expect(view.canSend()).toBe(false)
    expect(view.hasComposer()).toBe(true)
    expect(view.unavailableReason()).toMatch(/expired/)
    expect(await view.send('generated text')).toBe(false)
  })

  it('disables writes offline and requires fresh inspection on reconnect', async () => {
    const next = deferred<InspectNativeSessionControlResponse>()
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    view.setOnline(false)
    expect(view.canSend()).toBe(false)
    expect(view.hasComposer()).toBe(true)
    expect(view.unavailableReason()).toMatch(/Computer offline/)
    mocks.inspect.mockReturnValueOnce(next.promise)
    view.setOnline(true)
    expect(view.canSend()).toBe(false)
    next.resolve(capability({ bindingToken: 'generated-new-owner-binding' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(view.canSend()).toBe(true)
    await view.send('generated after reconnect')
    expect(mocks.send.mock.calls[0]![1].bindingToken).toBe('generated-new-owner-binding')
  })

  it('ignores old inspection and write results after switching conversations', async () => {
    const oldProbe = deferred<InspectNativeSessionControlResponse>()
    mocks.inspect.mockReturnValueOnce(oldProbe.promise)
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const second = { ...ref, sessionId: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' }
    view.setConversation(second)
    await vi.advanceTimersByTimeAsync(0)
    oldProbe.resolve(capability())
    await vi.advanceTimersByTimeAsync(0)
    expect(view.capabilities()?.sessionId).toBe(second.sessionId)
    const oldSend = deferred<{ sessionId: string, requestId: string, disposition: NativeSessionInputDisposition }>()
    mocks.send.mockReturnValueOnce(oldSend.promise)
    const sending = view.send('generated old draft')
    const request = mocks.send.mock.calls[0]![1]
    view.setConversation(ref)
    await vi.advanceTimersByTimeAsync(0)
    oldSend.resolve({ sessionId: second.sessionId, requestId: request.requestId, disposition: NativeSessionInputDisposition.ACCEPTED })
    expect(await sending).toBe(false)
    expect(view.receipt()).toBe('')
    expect(view.pending()).toBeUndefined()
  })

  it('interrupts only the verified active turn after an explicit call', async () => {
    mocks.inspect.mockResolvedValue(capability({ canInterrupt: true, activeTurnId: 'generated-turn', activityState: AgentActivityState.WORKING }))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.interrupt).not.toHaveBeenCalled()
    expect(view.canInterrupt()).toBe(true)
    await view.interrupt()
    expect(mocks.interrupt).toHaveBeenCalledWith(ref.workerId, { agentProvider: ref.agentProvider, sessionId: ref.sessionId, bindingToken: 'generated-binding', expectedTurnId: 'generated-turn', requestId: expect.any(String) })
    expect(view.receipt()).toMatch(/Interrupt accepted/)
    mocks.inspect.mockResolvedValue(capability({ canInterrupt: true, activeTurnId: '' }))
    await view.refresh()
    expect(view.canInterrupt()).toBe(false)
    await view.interrupt()
    expect(mocks.interrupt).toHaveBeenCalledOnce()
  })

  it('bounds unconfirmed writes without polling them into automatic retries', async () => {
    mocks.send.mockReturnValueOnce(new Promise(() => {}))
    const view = setup()
    await vi.advanceTimersByTimeAsync(0)
    const sending = view.send('generated timeout text')
    await vi.advanceTimersByTimeAsync(15_001)
    expect(await sending).toBe(false)
    expect(view.pending()).toBeUndefined()
    expect(view.error()).toMatch(/did not confirm delivery/)
    expect(mocks.send).toHaveBeenCalledOnce()
  })
})
