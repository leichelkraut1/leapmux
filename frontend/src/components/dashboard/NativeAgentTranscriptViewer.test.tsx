import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { installControllableResizeObserver } from '~/test-support/resizeObserverStub'

const hooks = vi.hoisted(() => ({ transcript: vi.fn() }))
vi.mock('~/hooks/useNativeAgentTranscript', () => ({ useNativeAgentTranscript: (...args: unknown[]) => hooks.transcript(...args) }))
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
})
