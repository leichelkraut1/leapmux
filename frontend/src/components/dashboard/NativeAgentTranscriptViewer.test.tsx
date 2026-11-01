import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import type { NativeSessionApproval } from '~/generated/proto/leapmux/v1/worker_pb'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { NativeSessionApprovalOptionSchema, NativeSessionApprovalSchema, NativeSessionInputBehavior } from '~/generated/proto/leapmux/v1/worker_pb'
import { deferred } from '~/test-support/async'
import { testMessageContext } from '~/test-support/messageContext'
import { installControllableResizeObserver } from '~/test-support/resizeObserverStub'

const hooks = vi.hoisted(() => ({ transcript: vi.fn(), control: vi.fn() }))
vi.mock('~/hooks/useNativeAgentTranscript', () => ({ useNativeAgentTranscript: (...args: unknown[]) => hooks.transcript(...args) }))
vi.mock('~/hooks/useNativeAgentControl', () => ({ useNativeAgentControl: (...args: unknown[]) => hooks.control(...args) }))
vi.mock('~/lib/shikiWorkerClient', () => ({ tokenizeAsync: vi.fn().mockResolvedValue(null) }))
const { NativeAgentTranscriptViewer } = await import('./NativeAgentTranscriptViewer')
const prototype = HTMLDialogElement.prototype
const originalShow = Object.getOwnPropertyDescriptor(prototype, 'showModal')
const originalClose = Object.getOwnPropertyDescriptor(prototype, 'close')
beforeAll(() => {
  installControllableResizeObserver()
  Object.defineProperties(prototype, {
    showModal: { configurable: true, writable: true, value(this: HTMLDialogElement) { this.setAttribute('open', '') } },
    close: { configurable: true, writable: true, value(this: HTMLDialogElement) { this.removeAttribute('open') } },
  })
})
afterEach(cleanup)
afterAll(() => {
  if (originalShow)
    Object.defineProperty(prototype, 'showModal', originalShow)
  else Reflect.deleteProperty(prototype, 'showModal')
  if (originalClose)
    Object.defineProperty(prototype, 'close', originalClose)
  else Reflect.deleteProperty(prototype, 'close')
})
const conversation: NativeConversationRef = { workerId: 'fixture-pc', workerName: 'Fixture PC', agentProvider: AgentProvider.CLAUDE_CODE, sessionId: '11111111-2222-3333-4444-555555555555' }

function fixture(empty = false) {
  controlFixture()
  const messages = empty
    ? []
    : [create(AgentChatMessageSchema, { id: 'generated-user', seq: 1n, source: MessageSource.USER, agentProvider: AgentProvider.CLAUDE_CODE, agentSessionId: conversation.sessionId, contentCompression: ContentCompression.NONE, content: new TextEncoder().encode(JSON.stringify({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: 'Harmless generated user message' }] } })) })]
  const [busy, setBusy] = createSignal(false)
  const refresh = vi.fn(async () => undefined)
  const older = vi.fn(async () => undefined)
  const resolver = testMessageContext({ messages: () => messages })
  hooks.transcript.mockReturnValue({ messages: () => messages, version: () => 0, title: () => 'Generated chat title', workingDir: () => '/generated', observedAt: () => '2026-10-28T12:00:00Z', partialReason: () => '', error: () => '', busy, fetchingOlder: () => false, hasOlder: () => !empty, hasNewer: () => false, loaded: () => true, sourceChanged: () => false, resolver: () => resolver, refresh, loadOlder: older })
  return { refresh, older, setBusy }
}

function controlFixture(supported = false, interruptible = false, inputBehavior = NativeSessionInputBehavior.QUEUE_NEXT_TURN) {
  const [canSend, setCanSend] = createSignal(supported)
  const [pending, setPending] = createSignal<'send' | 'interrupt' | 'approval'>()
  const [pendingApprovals, setApprovals] = createSignal<NativeSessionApproval[]>([])
  const [answerStates, setAnswerStates] = createSignal<Record<string, 'sending' | 'accepted' | 'uncertain'>>({})
  const [error, setError] = createSignal('')
  const [receipt, setReceipt] = createSignal('')
  const [recoverableText, setRecoverableText] = createSignal('')
  const send = vi.fn(async (_text: string) => true)
  const interrupt = vi.fn(async () => undefined)
  const refresh = vi.fn(async () => undefined)
  const answerApproval = vi.fn(async (_prompt: string, _option: string) => true)
  const canAnswerApproval = (promptId: string) => !pending() && !answerStates()[promptId] && pendingApprovals().some(prompt => prompt.promptId === promptId && prompt.canAnswer)
  hooks.control.mockReturnValue({
    capabilities: () => supported || pendingApprovals().length > 0 ? { ownerLabel: 'Generated CLI owner', transportLabel: 'Generated bridge', canInterrupt: interruptible, inputBehavior } : undefined,
    checking: () => false,
    connected: () => canSend() || pendingApprovals().some(prompt => prompt.canAnswer),
    canSend,
    canInterrupt: () => interruptible && canSend(),
    hasComposer: () => supported,
    inputBehavior: () => inputBehavior,
    unavailableReason: () => 'The original app does not have a control bridge.',
    pending,
    error,
    receipt,
    recoverableText,
    pendingApprovals,
    canAnswer: () => pendingApprovals().some(prompt => canAnswerApproval(prompt.promptId)),
    canAnswerApproval,
    approvalAnswerState: (promptId: string) => answerStates()[promptId],
    answerApproval,
    send,
    interrupt,
    refresh,
  })
  return { setCanSend, setPending, setError, setReceipt, setRecoverableText, setApprovals, setAnswerStates, answerApproval, send, interrupt, refresh }
}

function approvalFixture() {
  return create(NativeSessionApprovalSchema, {
    promptId: 'generated-prompt',
    title: 'Run generated command?',
    detail: 'Command: printf "harmless generated text"\nWorking directory: /generated\n<script>literal, not executable</script>',
    canAnswer: true,
    options: [
      { id: 'generated-allow-once', label: 'Allow once', description: 'Allow only this tool call.' },
      { id: 'generated-deny', label: 'Deny', description: 'Do not allow this tool call.' },
    ],
  })
}

describe('NativeAgentTranscriptViewer', () => {
  it('shows source identity and read-only history without any chat mutation affordance', () => {
    const state = fixture()
    const [name, setName] = createSignal('Fixture PC')
    const view = render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} workerName={name()} online onClose={() => {}} /></PreferencesProvider>)
    expect(screen.getByRole('dialog', { name: 'Generated chat title' })).toBeInTheDocument()
    expect(screen.getByText('Claude Code · Fixture PC')).toBeInTheDocument()
    expect(screen.getByText(/Read only · This chat remains controlled by its original app/)).toBeInTheDocument()
    const history = screen.getByTestId('native-transcript-history')
    setName('Hydrated computer name')
    expect(screen.getByText('Claude Code · Hydrated computer name')).toBeInTheDocument()
    expect(screen.getByTestId('native-transcript-history')).toBe(history)
    expect(view.container.querySelector('[contenteditable="true"],textarea')).toBeNull()
    expect(screen.queryByRole('button', { name: /send|approve|interrupt|resume|quote/i })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh native chat history' }))
    expect(state.refresh).toHaveBeenCalledOnce()
    state.setBusy(true)
    expect(screen.getByRole('button', { name: 'Refresh native chat history' })).toBeDisabled()
    expect(screen.getByRole('dialog', { name: 'Generated chat title' })).toBeInTheDocument()
  })

  it('uses its own read-only empty state and retains dated offline information', () => {
    fixture(true)
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online={false} onClose={() => {}} /></PreferencesProvider>)
    expect(screen.getByText('No readable messages in this history page.')).toBeInTheDocument()
    expect(screen.getByText('Computer offline. Showing the last loaded history.')).toBeInTheDocument()
    expect(screen.getByText('Last checked').querySelector('time')).toHaveAttribute('datetime', '2026-10-28T12:00:00Z')
    expect(screen.queryByText(/Send a message to start/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh native chat history' })).toBeDisabled()
  })

  it('continues the original chat with an accessible plaintext composer and explicit interrupt', async () => {
    const transcript = fixture()
    const state = controlFixture(true, true)
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    expect(screen.getByText('Owner: Generated CLI owner · Generated bridge')).toBeInTheDocument()
    const editor = screen.getByRole('textbox', { name: 'Message to original chat' })
    const send = screen.getByRole('button', { name: 'Send to original chat' })
    expect(send).toBeDisabled()
    fireEvent.input(editor, { target: { value: 'Harmless generated next message' } })
    expect(send).toBeEnabled()
    await fireEvent.submit(editor.closest('form')!)
    expect(state.send).toHaveBeenCalledWith('Harmless generated next message')
    expect(editor).toHaveValue('')
    expect(transcript.refresh).toHaveBeenCalledOnce()
    expect(state.interrupt).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Interrupt current turn' }))
    expect(state.interrupt).toHaveBeenCalledOnce()
    state.setReceipt('Message queued in the original app for its next turn.')
    expect(screen.getByText('Message queued in the original app for its next turn.')).toHaveAttribute('role', 'status')
    expect(screen.queryByRole('button', { name: /resume|take over|approve/i })).not.toBeInTheDocument()
  })

  it('retains text after an unconfirmed send and disables sending when the connection is lost', async () => {
    fixture()
    const state = controlFixture(true)
    state.send.mockResolvedValueOnce(false)
    const [online, setOnline] = createSignal(true)
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online={online()} onClose={() => {}} /></PreferencesProvider>)
    const editor = screen.getByRole('textbox', { name: 'Message to original chat' })
    fireEvent.input(editor, { target: { value: 'Generated retained draft' } })
    await fireEvent.submit(editor.closest('form')!)
    state.setError('The original app did not confirm delivery. Your text is kept.')
    expect(editor).toHaveValue('Generated retained draft')
    expect(screen.getByRole('alert')).toHaveTextContent('did not confirm delivery')
    state.setCanSend(false)
    setOnline(false)
    expect(editor).toBeEnabled()
    expect(editor).toHaveValue('Generated retained draft')
    expect(screen.getByRole('button', { name: 'Send to original chat' })).toBeDisabled()
    expect(screen.getByText('Computer offline. Showing the last loaded history.')).toBeInTheDocument()
    expect(screen.getByTestId('native-transcript-history')).toBeInTheDocument()
  })

  it('keeps edits made while a previous message waits for acknowledgement', async () => {
    fixture()
    const state = controlFixture(true)
    const gate = deferred<boolean>()
    state.send.mockReturnValueOnce(gate.promise)
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    const editor = screen.getByRole('textbox', { name: 'Message to original chat' })
    fireEvent.input(editor, { target: { value: 'Generated first message' } })
    fireEvent.submit(editor.closest('form')!)
    state.setPending('send')
    expect(screen.getByRole('button', { name: 'Sending…' })).toBeDisabled()
    fireEvent.input(editor, { target: { value: 'Generated next draft' } })
    gate.resolve(true)
    await Promise.resolve()
    expect(editor).toHaveValue('Generated next draft')
  })

  it('measures the message limit in UTF-8 bytes and preserves oversized text for editing', () => {
    fixture()
    const state = controlFixture(true)
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    const editor = screen.getByRole('textbox', { name: 'Message to original chat' })
    fireEvent.input(editor, { target: { value: 'é'.repeat(32_769) } })
    expect(editor).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Send to original chat' })).toBeDisabled()
    expect(state.send).not.toHaveBeenCalled()
    fireEvent.input(editor, { target: { value: 'é'.repeat(32_768) } })
    expect(editor).toHaveAttribute('aria-invalid', 'false')
    expect(screen.getByRole('button', { name: 'Send to original chat' })).toBeEnabled()
  })

  it('restores a dropped message only into an empty editor without sending it', () => {
    fixture()
    const state = controlFixture(true)
    state.setRecoverableText('Generated dropped message')
    state.setReceipt('The original app dropped this message. It was not resent.')
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    fireEvent.click(screen.getByRole('button', { name: 'Restore message to editor' }))
    expect(screen.getByRole('textbox', { name: 'Message to original chat' })).toHaveValue('Generated dropped message')
    expect(state.send).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Restore message to editor' })).not.toBeInTheDocument()
  })

  it('labels native submission as send now and explicitly describes its effect on an active turn', async () => {
    fixture()
    const state = controlFixture(true, false, NativeSessionInputBehavior.SEND_NOW)
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    const editor = screen.getByRole('textbox', { name: 'Message to original chat' })
    expect(editor).toHaveAccessibleDescription(/If this chat is running, this sends input to its current turn/)
    expect(screen.queryByText('Messages queue in the original app for its next turn.')).not.toBeInTheDocument()
    fireEvent.input(editor, { target: { value: 'Generated deliberate immediate input' } })
    await fireEvent.click(screen.getByRole('button', { name: 'Send now' }))
    expect(state.send).toHaveBeenCalledWith('Generated deliberate immediate input')
  })

  it('shows exact approval details and scope choices and submits only the explicitly clicked option', async () => {
    fixture()
    const state = controlFixture()
    const prompt = approvalFixture()
    state.setApprovals([prompt])
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    const card = screen.getByRole('region', { name: 'Approval: Run generated command?' })
    expect(card).toHaveTextContent('Waiting for your approval')
    expect(card.querySelector('pre')?.textContent).toBe(prompt.detail)
    expect(card.querySelector('script')).toBeNull()
    expect(screen.getByText('Allow only this tool call.')).toBeInTheDocument()
    expect(screen.getByText('Do not allow this tool call.')).toBeInTheDocument()
    expect(screen.getByText(/Connected to the original conversation/)).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(state.answerApproval).not.toHaveBeenCalled()
    await fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
    expect(state.answerApproval).toHaveBeenCalledExactlyOnceWith('generated-prompt', 'generated-allow-once')
    expect(state.send).not.toHaveBeenCalled()
    expect(state.interrupt).not.toHaveBeenCalled()
    state.setPending('approval')
    state.setAnswerStates({ 'generated-prompt': 'sending' })
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled()
    expect(screen.getByText('Sending your answer…')).toBeInTheDocument()
    state.setPending(undefined)
    state.setAnswerStates({ 'generated-prompt': 'accepted' })
    expect(screen.getByText('Answer submitted to the original app. Waiting for the prompt to close.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled()
    state.setReceipt('Answer submitted to the original app. Its permission rules still apply.')
    expect(screen.getByText('Answer submitted to the original app. Its permission rules still apply.')).toHaveAttribute('role', 'status')
    state.setError('Generated approval-only refusal is visible.')
    expect(screen.getByRole('alert')).toHaveTextContent('Generated approval-only refusal is visible.')
  })

  it('retains approval cards during polling and removes locally resolved prompts without clearing a draft', () => {
    fixture()
    const state = controlFixture(true)
    const prompt = approvalFixture()
    state.setApprovals([prompt])
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    const card = screen.getByRole('region', { name: 'Approval: Run generated command?' })
    const allow = screen.getByRole('button', { name: 'Allow once' })
    const history = screen.getByTestId('native-transcript-history')
    const editor = screen.getByRole('textbox', { name: 'Message to original chat' })
    fireEvent.input(editor, { target: { value: 'Generated retained draft' } })
    state.setApprovals([create(NativeSessionApprovalSchema, { ...prompt, detail: `${prompt.detail}\nUpdated generated detail.` })])
    expect(screen.getByRole('region', { name: 'Approval: Run generated command?' })).toBe(card)
    expect(screen.getByRole('button', { name: 'Allow once' })).toBe(allow)
    expect(card).toHaveTextContent('Updated generated detail.')
    state.setApprovals([])
    expect(screen.queryByTestId('native-approval-prompts')).not.toBeInTheDocument()
    expect(screen.getByTestId('native-transcript-history')).toBe(history)
    expect(editor).toHaveValue('Generated retained draft')
    expect(state.answerApproval).not.toHaveBeenCalled()
  })

  it('unmounts replaced prompt and option buttons so an old gesture cannot answer a new request', () => {
    fixture()
    const state = controlFixture(true)
    const original = approvalFixture()
    state.setApprovals([original])
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    const oldCard = screen.getByRole('region', { name: 'Approval: Run generated command?' })
    const oldAllow = screen.getByRole('button', { name: 'Allow once' })
    fireEvent.pointerDown(oldAllow)
    state.setApprovals([create(NativeSessionApprovalSchema, { ...original, promptId: 'generated-replacement' })])
    const newAllow = screen.getByRole('button', { name: 'Allow once' })
    expect(oldCard.isConnected).toBe(false)
    expect(oldAllow.isConnected).toBe(false)
    expect(newAllow).not.toBe(oldAllow)
    fireEvent.pointerUp(oldAllow)
    fireEvent.click(oldAllow)
    expect(state.answerApproval).not.toHaveBeenCalled()
    const nextOptions = [create(NativeSessionApprovalOptionSchema, { id: 'generated-new-allow', label: 'Allow once', description: 'Allow only the new generated request.' })]
    fireEvent.pointerDown(newAllow)
    state.setApprovals([create(NativeSessionApprovalSchema, { ...original, promptId: 'generated-replacement', options: nextOptions })])
    const latestAllow = screen.getByRole('button', { name: 'Allow once' })
    expect(newAllow.isConnected).toBe(false)
    expect(latestAllow).not.toBe(newAllow)
    fireEvent.pointerUp(newAllow)
    fireEvent.click(newAllow)
    expect(state.answerApproval).not.toHaveBeenCalled()
    fireEvent.click(latestAllow)
    expect(state.answerApproval).toHaveBeenCalledExactlyOnceWith('generated-replacement', 'generated-new-allow')
  })

  it('keeps unsupported approval details visible with disabled choices and the original app explanation', () => {
    fixture()
    const state = controlFixture()
    state.setApprovals([create(NativeSessionApprovalSchema, { ...approvalFixture(), canAnswer: false, unavailableReason: 'Answer this generated unsupported prompt in the original app.' })])
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    expect(screen.getByRole('region', { name: 'Approval: Run generated command?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled()
    expect(screen.getByText('Answer this generated unsupported prompt in the original app.')).toBeInTheDocument()
    expect(state.answerApproval).not.toHaveBeenCalled()
  })

  it('blocks all choices after an uncertain answer while keeping the full prompt available for review', () => {
    fixture()
    const state = controlFixture(true)
    const prompt = approvalFixture()
    state.setApprovals([prompt])
    state.setAnswerStates({ 'generated-prompt': 'uncertain' })
    render(() => <PreferencesProvider><NativeAgentTranscriptViewer conversation={conversation} online onClose={() => {}} /></PreferencesProvider>)
    expect(screen.getByText('Answer unconfirmed. It will not be resent; check the original app.')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Approval: Run generated command?' }).querySelector('pre')?.textContent).toBe(prompt.detail)
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled()
    expect(state.answerApproval).not.toHaveBeenCalled()
  })
})
