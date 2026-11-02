import type { NativeConversationDestination } from './AttachNativeConversationDialog'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import { cleanup, fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickMenuValue } from '~/test-support/menu'
import { AttachNativeConversationDialog } from './AttachNativeConversationDialog'

const conversation: NativeConversationRef = { workerId: 'source-pc', workerName: 'Source PC', agentProvider: AgentProvider.CODEX, sessionId: '11111111-2222-3333-4444-555555555555' }
const first: NativeConversationDestination = { id: 'fixture-one', workspaceId: 'workspace-one', workspaceName: 'First workspace', workerId: 'destination-pc', workerName: 'Destination PC', workingDir: '/generated/first', label: 'feature-one — first', branchName: 'feature-one' }
const second: NativeConversationDestination = { ...first, id: 'fixture-two', workspaceId: 'workspace-two', workspaceName: 'Second workspace', workingDir: '/generated/second', label: 'feature-two — second', branchName: 'feature-two' }
const dialogPrototype = HTMLDialogElement.prototype
const originalShow = Object.getOwnPropertyDescriptor(dialogPrototype, 'showModal')
const originalClose = Object.getOwnPropertyDescriptor(dialogPrototype, 'close')

beforeAll(() => {
  Object.defineProperties(dialogPrototype, {
    showModal: { configurable: true, writable: true, value(this: HTMLDialogElement) { this.setAttribute('open', '') } },
    close: { configurable: true, writable: true, value(this: HTMLDialogElement) { this.removeAttribute('open') } },
  })
})
afterEach(cleanup)
afterAll(() => {
  if (originalShow)
    Object.defineProperty(dialogPrototype, 'showModal', originalShow)
  else Reflect.deleteProperty(dialogPrototype, 'showModal')
  if (originalClose)
    Object.defineProperty(dialogPrototype, 'close', originalClose)
  else Reflect.deleteProperty(dialogPrototype, 'close')
})

describe('AttachNativeConversationDialog', () => {
  it('identifies the source and submits only the selected existing worktree', () => {
    const onConfirm = vi.fn()
    const view = render(() => <AttachNativeConversationDialog conversation={conversation} destinations={[first, second]} onConfirm={onConfirm} onClose={() => {}} />)
    expect(screen.getByText(/Codex · Source PC/)).toBeInTheDocument()
    expect(screen.getByText(/keeps its original computer and working folder/)).toBeInTheDocument()
    expect(view.container.querySelector('select')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Add to worktree' }))
    expect(onConfirm).toHaveBeenCalledExactlyOnceWith(first)
  })

  it('chooses a workspace before filtering and choosing one of its worktrees', () => {
    const alternate = { ...second, id: 'fixture-three', workingDir: '/generated/alternate', label: 'alternate branch' }
    const onConfirm = vi.fn()
    render(() => <AttachNativeConversationDialog conversation={conversation} destinations={[first, second, alternate]} onConfirm={onConfirm} onClose={() => {}} />)
    pickMenuValue('native-conversation-workspace-menu', second.workspaceId)
    const menu = within(screen.getByTestId('native-conversation-worktree-menu'))
    expect(menu.queryByRole('menuitemradio', { name: /feature-one/, hidden: true })).not.toBeInTheDocument()
    fireEvent.input(menu.getByRole('searchbox', { name: 'Filter worktrees', hidden: true }), { target: { value: 'alternate' } })
    expect(menu.queryByRole('menuitemradio', { name: /feature-two/, hidden: true })).not.toBeInTheDocument()
    fireEvent.click(menu.getByRole('menuitemradio', { name: 'alternate branch · Destination PC', hidden: true }))
    fireEvent.click(screen.getByRole('button', { name: 'Add to worktree' }))
    expect(onConfirm).toHaveBeenCalledExactlyOnceWith(alternate)
  })

  it('keeps offline destinations visible and disabled', () => {
    const offline = { ...first, disabledReason: 'Destination PC is offline. Reconnect it to save this link.' }
    const onConfirm = vi.fn()
    render(() => <AttachNativeConversationDialog conversation={conversation} destinations={[offline]} onConfirm={onConfirm} onClose={() => {}} />)
    expect(screen.getByRole('status')).toHaveTextContent(offline.disabledReason)
    expect(within(screen.getByTestId('native-conversation-worktree-menu')).getByRole('menuitemradio', { hidden: true })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Add to worktree' })).toBeDisabled()
    fireEvent.submit(screen.getByRole('button', { name: 'Add to worktree' }).closest('form')!)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('keeps enter in a worktree filter from submitting the destination', () => {
    const onConfirm = vi.fn()
    render(() => <AttachNativeConversationDialog conversation={conversation} destinations={[first]} onConfirm={onConfirm} onClose={() => {}} />)
    const filter = within(screen.getByTestId('native-conversation-worktree-menu')).getByRole('searchbox', { name: 'Filter worktrees', hidden: true })
    const event = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    filter.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('refuses a removed destination instead of silently choosing another', () => {
    const [destinations, setDestinations] = createSignal([first, { ...first, id: 'fixture-other', workingDir: '/generated/other', label: 'other worktree' }])
    const onConfirm = vi.fn()
    render(() => <AttachNativeConversationDialog conversation={conversation} destinations={destinations()} onConfirm={onConfirm} onClose={() => {}} />)
    setDestinations(current => current.filter(item => item.id !== first.id))
    expect(screen.getByRole('status')).toHaveTextContent('The selected worktree is no longer available.')
    expect(screen.getByRole('button', { name: 'Add to worktree' })).toBeDisabled()
    fireEvent.submit(screen.getByRole('button', { name: 'Add to worktree' }).closest('form')!)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('reports an empty destination list and cancels without attaching', () => {
    const onConfirm = vi.fn()
    const onClose = vi.fn()
    render(() => <AttachNativeConversationDialog conversation={conversation} destinations={[]} onConfirm={onConfirm} onClose={onClose} />)
    expect(screen.getByRole('status')).toHaveTextContent('Open a worktree in a LeapMux workspace first')
    expect(screen.getByRole('button', { name: 'Add to worktree' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledOnce()
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('blocks repeated submission while saving and keeps the destination after an error', () => {
    const [busy, setBusy] = createSignal(true)
    const onConfirm = vi.fn()
    render(() => <AttachNativeConversationDialog conversation={conversation} destinations={[first]} busy={busy()} error="Generated save error" onConfirm={onConfirm} onClose={() => {}} />)
    expect(screen.getByRole('button', { name: 'Adding…' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent('Generated save error')
    fireEvent.submit(screen.getByRole('button', { name: 'Adding…' }).closest('form')!)
    expect(onConfirm).not.toHaveBeenCalled()
    setBusy(false)
    fireEvent.click(screen.getByRole('button', { name: 'Add to worktree' }))
    expect(onConfirm).toHaveBeenCalledExactlyOnceWith(first)
  })
})
