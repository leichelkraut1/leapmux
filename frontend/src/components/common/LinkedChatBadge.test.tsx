import type { Tab } from '~/stores/tab.types'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { hoverForTooltip, unhoverTooltip } from '~/test-support/clipStub'
import { LinkedChatBadge } from './LinkedChatBadge'

const store = vi.hoisted(() => ({ workerInfo: vi.fn() }))
vi.mock('~/stores/workerInfo.store', () => ({ workerInfoStore: { workerInfo: (...args: unknown[]) => store.workerInfo(...args) } }))
const native: Tab = { id: 'native-fixture', type: TabType.NATIVE_CONVERSATION, workspaceId: 'generated-workspace', workerId: 'destination-pc', sourceWorkerId: 'source-pc', agentProvider: AgentProvider.CODEX, sessionId: 'generated-session' }

beforeEach(() => {
  vi.useFakeTimers()
  store.workerInfo.mockReset().mockReturnValue(null)
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('LinkedChatBadge', () => {
  it('marks only linked chats and reads the source computer name', () => {
    store.workerInfo.mockReturnValue({ name: 'Source PC' })
    render(() => <LinkedChatBadge tab={native} />)
    const badge = screen.getByTestId('linked-chat-badge')
    expect(badge).toHaveTextContent('Linked')
    expect(badge.querySelector('svg')).toBeInTheDocument()
    expect(hoverForTooltip(badge)).toHaveTextContent('This conversation stays in its original app on Source PC.')
    expect(store.workerInfo).toHaveBeenCalledWith('source-pc')
    expect(store.workerInfo).not.toHaveBeenCalledWith('destination-pc')
  })

  it('describes a source whose computer name has not loaded and updates when it arrives', () => {
    const [name, setName] = createSignal('')
    store.workerInfo.mockImplementation(() => name() ? { name: name() } : null)
    render(() => <LinkedChatBadge tab={native} />)
    const badge = screen.getByTestId('linked-chat-badge')
    expect(hoverForTooltip(badge)).toHaveTextContent('original app and on its source computer')
    unhoverTooltip(badge)
    setName('Hydrated Source PC')
    expect(hoverForTooltip(badge)).toHaveTextContent('on Hydrated Source PC.')
    expect(screen.getByTestId('linked-chat-badge')).toBe(badge)
  })

  it('does not mark a LeapMux-managed agent or another tab type', () => {
    const managed: Tab = { id: 'agent-fixture', type: TabType.AGENT, workspaceId: 'generated-workspace', agentProvider: AgentProvider.CODEX }
    const [tab, setTab] = createSignal<Tab>(managed)
    render(() => <LinkedChatBadge tab={tab()} />)
    expect(screen.queryByTestId('linked-chat-badge')).not.toBeInTheDocument()
    setTab({ id: 'terminal-fixture', type: TabType.TERMINAL, workspaceId: 'generated-workspace' })
    expect(screen.queryByTestId('linked-chat-badge')).not.toBeInTheDocument()
    expect(store.workerInfo).not.toHaveBeenCalled()
  })
})
