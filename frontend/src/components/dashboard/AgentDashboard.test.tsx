import type { DashboardAgent } from '~/stores/agentDashboard'
import type { DashboardExternalWorker } from '~/stores/externalAgentInventory'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentDashboard } from './AgentDashboard'

const dialogPrototype = HTMLDialogElement.prototype
const originalShowModal = Object.getOwnPropertyDescriptor(dialogPrototype, 'showModal')
const originalClose = Object.getOwnPropertyDescriptor(dialogPrototype, 'close')

beforeAll(() => {
  Object.defineProperties(dialogPrototype, {
    showModal: {
      configurable: true,
      writable: true,
      value(this: HTMLDialogElement) { this.setAttribute('open', '') },
    },
    close: {
      configurable: true,
      writable: true,
      value(this: HTMLDialogElement) { this.removeAttribute('open') },
    },
  })
})

afterEach(cleanup)

afterAll(() => {
  if (originalShowModal)
    Object.defineProperty(dialogPrototype, 'showModal', originalShowModal)
  else
    Reflect.deleteProperty(dialogPrototype, 'showModal')
  if (originalClose)
    Object.defineProperty(dialogPrototype, 'close', originalClose)
  else
    Reflect.deleteProperty(dialogPrototype, 'close')
})

const rows: DashboardAgent[] = [
  { agentId: 'waiting-agent', workerId: 'mac', workerName: 'Mac Studio', workspaceId: 'factory', workspaceName: 'Factory tools', title: 'Clarify setup', providerName: 'Claude', status: 'waiting' },
  { agentId: 'working-agent', workerId: 'windows', workerName: 'Windows PC', workspaceId: 'trading', workspaceName: 'TradingDesk', title: 'Review pipeline', providerName: 'Codex', status: 'working' },
  { agentId: 'idle-agent', workerId: 'windows', workerName: 'Windows PC', workspaceId: 'dashboard', workspaceName: 'Dashboard', title: 'Summarize notes', providerName: 'Gemini', status: 'idle' },
  { agentId: 'offline-agent', workerId: 'linux', workerName: 'Linux box', workspaceId: 'website', workspaceName: 'Website', title: 'Offline session', providerName: 'Claude', status: 'offline' },
  { agentId: 'unknown-agent', workerId: 'laptop', workerName: 'Laptop', workspaceId: 'infra', workspaceName: 'Infrastructure', title: 'Unreported session', providerName: 'Codex', status: 'unknown' },
]

function renderDashboard(items: readonly DashboardAgent[] = rows) {
  const onOpenAgent = vi.fn()
  const onClose = vi.fn()
  render(() => <AgentDashboard rows={items} onOpenAgent={onOpenAgent} onClose={onClose} />)
  return { onOpenAgent, onClose }
}

function agentList() {
  return within(screen.getByRole('list', { name: 'Agents' }))
}

describe('AgentDashboard', () => {
  it('shows agents from multiple computers and keeps unavailable agents out of the idle total', () => {
    renderDashboard()
    expect(screen.getByRole('dialog', { name: 'Agents dashboard' })).toBeInTheDocument()
    expect(agentList().getAllByRole('listitem')).toHaveLength(5)
    expect(agentList().getByText('Mac Studio')).toBeInTheDocument()
    expect(agentList().getAllByText('Windows PC')).toHaveLength(2)
    expect(agentList().getByText('Factory tools')).toBeInTheDocument()
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working1')
    expect(screen.getByTestId('agent-count-waiting')).toHaveTextContent('Waiting on you1')
    expect(screen.getByTestId('agent-count-idle')).toHaveTextContent('Idle1')
    expect(screen.getByTestId('agent-count-unavailable')).toHaveTextContent('Unavailable2')
    expect(agentList().getByText('Offline', { exact: true })).toBeInTheDocument()
    expect(agentList().getByText('Status unavailable')).toBeInTheDocument()
  })

  it('filters working, waiting, and idle agents while all includes unavailable agents', () => {
    renderDashboard()
    for (const [label, title] of [
      ['Working', 'Review pipeline'],
      ['Waiting on you', 'Clarify setup'],
      ['Idle', 'Summarize notes'],
    ] as const) {
      fireEvent.click(screen.getByRole('radio', { name: label }))
      expect(agentList().getAllByRole('listitem')).toHaveLength(1)
      expect(agentList().getByRole('heading', { name: title })).toBeInTheDocument()
    }
    fireEvent.click(screen.getByRole('radio', { name: 'All' }))
    expect(agentList().getAllByRole('listitem')).toHaveLength(5)
  })

  it.each([
    ['REVIEW PIPELINE', 'Review pipeline'],
    ['tradingdesk', 'Review pipeline'],
    ['gemini', 'Summarize notes'],
    ['  mac studio  ', 'Clarify setup'],
  ])('searches agent, workspace, provider, and computer names for %s', (query, title) => {
    renderDashboard()
    fireEvent.input(screen.getByRole('searchbox', { name: 'Search agents' }), { target: { value: query } })
    expect(agentList().getAllByRole('listitem')).toHaveLength(1)
    expect(agentList().getByRole('heading', { name: title })).toBeInTheDocument()
  })

  it('combines search with the selected status and distinguishes an empty result', () => {
    renderDashboard()
    fireEvent.click(screen.getByRole('radio', { name: 'Working' }))
    fireEvent.input(screen.getByRole('searchbox', { name: 'Search agents' }), { target: { value: 'Mac Studio' } })
    expect(screen.getByText('No matching agents')).toBeInTheDocument()
    expect(screen.queryByText('No agents yet')).not.toBeInTheDocument()
    expect(screen.getByText('Showing 0 of 5 agents')).toBeInTheDocument()
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working1')
  })

  it('opens the exact selected agent, including its unavailable computer', () => {
    const { onOpenAgent } = renderDashboard()
    fireEvent.click(screen.getByRole('button', { name: 'Open chat: Offline session on Linux box' }))
    expect(onOpenAgent).toHaveBeenCalledExactlyOnceWith(rows[3])
  })

  it('updates live counts and filtered rows when the supplied activity changes', () => {
    const [liveRows, setLiveRows] = createSignal(rows)
    render(() => <AgentDashboard rows={liveRows()} onOpenAgent={vi.fn()} onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Working' }))
    setLiveRows(previous => previous.map(row => row.status === 'working' ? { ...row, status: 'idle' as const } : row))
    expect(screen.getByText('No matching agents')).toBeInTheDocument()
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working0')
    expect(screen.getByTestId('agent-count-idle')).toHaveTextContent('Idle2')
  })

  it('preserves the focused row action when live snapshots replace row objects', () => {
    const [liveRows, setLiveRows] = createSignal(rows)
    const onOpenAgent = vi.fn()
    render(() => <AgentDashboard rows={liveRows()} onOpenAgent={onOpenAgent} onClose={vi.fn()} />)
    const open = screen.getByRole('button', { name: 'Open chat: Review pipeline on Windows PC' })
    open.focus()
    setLiveRows(previous => previous.map(row => ({ ...row, status: row.status === 'working' ? 'idle' : row.status })))
    expect(open).toHaveFocus()
    expect(open).toBeInTheDocument()
    fireEvent.click(open)
    expect(onOpenAgent).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ agentId: 'working-agent', status: 'idle' }))
  })

  it('shows startup details without treating an unavailable agent as idle', () => {
    renderDashboard(rows.map(row => row.status === 'unknown' ? { ...row, statusDetail: 'Startup failed' } : row))
    expect(agentList().getByText('Startup failed')).toBeInTheDocument()
    expect(agentList().getByText('Status unavailable')).toBeInTheDocument()
    expect(screen.getByTestId('agent-count-idle')).toHaveTextContent('Idle1')
  })

  it('preserves the supplied priority order', () => {
    renderDashboard()
    expect(agentList().getAllByRole('heading').map(heading => heading.textContent))
      .toEqual(rows.map(row => row.title))
  })

  it('explains when there are no agents at all', () => {
    renderDashboard([])
    expect(screen.getByText('No agents yet')).toBeInTheDocument()
    expect(screen.queryByText('No matching agents')).not.toBeInTheDocument()
    expect(screen.getByTestId('agent-count-unavailable')).toHaveTextContent('Unavailable0')
  })

  it('passes the dialog close action to its caller', async () => {
    const { onClose } = renderDashboard()
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
  })

  it('counts and filters observed sessions across computers without counting native helper processes', () => {
    const onRefreshExternal = vi.fn()
    render(() => (
      <AgentDashboard
        rows={rows}
        externalWorkers={[{
          workerId: 'external',
          workerName: 'External PC',
          status: 'available',
          processes: [
            { pid: 100, providerName: 'Claude', processName: 'claude.exe', kind: 'cli', sessions: [
              { sessionId: 'working-external', status: 'working' },
              { sessionId: 'waiting-external', status: 'waiting' },
              { sessionId: 'idle-external', status: 'idle' },
              { sessionId: 'unknown-external', status: 'unknown' },
            ] },
            { pid: 200, providerName: 'Codex', processName: 'Codex', kind: 'desktop', sessions: [] },
          ],
        }]}
        onRefreshExternal={onRefreshExternal}
        onOpenAgent={vi.fn()}
        onClose={vi.fn()}
      />
    ))
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working2')
    expect(screen.getByTestId('agent-count-waiting')).toHaveTextContent('Waiting on you2')
    expect(screen.getByTestId('agent-count-idle')).toHaveTextContent('Idle2')
    expect(screen.getByTestId('agent-count-unavailable')).toHaveTextContent('Unavailable3')
    expect(screen.getByText('Showing 9 of 9 agents')).toBeInTheDocument()
    const external = within(screen.getByRole('region', { name: 'Outside LeapMux' }))
    expect(external.getByText('Processes without session observations (1)').closest('details')).not.toHaveAttribute('open')
    expect(external.queryByRole('button', { name: /Open chat/ })).not.toBeInTheDocument()

    for (const label of ['Working', 'Waiting on you', 'Idle']) {
      fireEvent.click(screen.getByRole('radio', { name: label }))
      expect(agentList().getAllByRole('listitem')).toHaveLength(1)
      expect(within(external.getByRole('list', { name: 'Observed sessions on External PC' })).getAllByRole('listitem')).toHaveLength(1)
      expect(external.getByText(label, { exact: true })).toBeInTheDocument()
      expect(external.queryByText('Process detected', { exact: true })).not.toBeInTheDocument()
      expect(screen.getByText('Showing 2 of 9 agents')).toBeInTheDocument()
    }

    fireEvent.click(external.getByRole('button', { name: 'Refresh external processes' }))
    expect(onRefreshExternal).toHaveBeenCalledOnce()
    fireEvent.input(screen.getByRole('searchbox', { name: 'Search agents' }), { target: { value: 'External PC' } })
    expect(external.getByText('Idle', { exact: true })).toBeInTheDocument()
    expect(screen.getByText('No matching agents')).toBeInTheDocument()
    expect(screen.getByText('Showing 1 of 9 agents')).toBeInTheDocument()
  })

  it.each(['loading', 'unavailable', 'offline'] as const)('excludes stale native session counts while a computer is %s', (status) => {
    render(() => (
      <AgentDashboard
        rows={rows}
        externalWorkers={[{
          workerId: 'external',
          workerName: 'External PC',
          status,
          processes: [{ pid: 100, providerName: 'Claude', processName: 'claude.exe', kind: 'cli', sessions: [
            { sessionId: 'stale-working', status: 'working' },
            { sessionId: 'stale-idle', status: 'idle' },
          ] }],
        }]}
        onOpenAgent={vi.fn()}
        onClose={vi.fn()}
      />
    ))
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working1')
    expect(screen.getByTestId('agent-count-idle')).toHaveTextContent('Idle1')
    expect(screen.getByText('Showing 5 of 5 agents')).toBeInTheDocument()
  })

  it('excludes old session counts when the computer registry becomes unavailable', () => {
    const [registryStatus, setRegistryStatus] = createSignal<'available' | 'unavailable'>('available')
    render(() => (
      <AgentDashboard
        rows={rows}
        registryStatus={registryStatus()}
        externalWorkers={[{
          workerId: 'external',
          workerName: 'External PC',
          status: 'available',
          processes: [{ pid: 100, providerName: 'Claude', processName: 'claude.exe', kind: 'cli', sessions: [
            { sessionId: 'active-external', status: 'working' },
          ] }],
        }]}
        onOpenAgent={vi.fn()}
        onClose={vi.fn()}
      />
    ))
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working2')
    setRegistryStatus('unavailable')
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working1')
    expect(screen.queryByRole('list', { name: 'Observed sessions on External PC' })).not.toBeInTheDocument()
  })

  it('keeps observed session totals stable while updating and clears them after failure', () => {
    const snapshot = {
      workerId: 'external',
      workerName: 'External PC',
      status: 'available' as const,
      observedAt: '2026-10-27T12:00:00Z',
      processes: [{ pid: 100, providerName: 'Claude', processName: 'claude.exe', kind: 'cli' as const, sessions: [
        { sessionId: 'active-external', status: 'working' as const },
      ] }],
    }
    const [externalWorkers, setExternalWorkers] = createSignal<readonly DashboardExternalWorker[]>([snapshot])
    render(() => (
      <AgentDashboard
        rows={rows}
        externalWorkers={externalWorkers()}
        onRefreshExternal={vi.fn()}
        onOpenAgent={vi.fn()}
        onClose={vi.fn()}
      />
    ))
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working2')
    setExternalWorkers([{ ...snapshot, refreshing: true }])
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working2')
    expect(screen.getByText('Showing 6 of 6 agents')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Updating observation…')
    expect(screen.getByText(/Observed at/).querySelector('time')).toHaveAttribute('datetime', snapshot.observedAt)
    expect(screen.getByRole('button', { name: 'Refresh external processes' })).toBeDisabled()
    setExternalWorkers([{ ...snapshot, status: 'unavailable', refreshing: false, processes: [], observedAt: undefined }])
    expect(screen.getByTestId('agent-count-working')).toHaveTextContent('Working1')
    expect(screen.getByText('Showing 5 of 5 agents')).toBeInTheDocument()
    expect(screen.queryByText('Updating observation…')).not.toBeInTheDocument()
    expect(screen.queryByText(/Observed at/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh external processes' })).toBeEnabled()
  })

  it('passes the computer registry state to the external observation section', () => {
    render(() => (
      <AgentDashboard
        rows={rows}
        externalWorkers={[]}
        registryStatus="unavailable"
        onRefreshExternal={vi.fn()}
        onOpenAgent={vi.fn()}
        onClose={vi.fn()}
      />
    ))
    expect(screen.getByRole('status')).toHaveTextContent('Computer list unavailable')
    expect(screen.queryByText(/No computers available/)).not.toBeInTheDocument()
  })
})
