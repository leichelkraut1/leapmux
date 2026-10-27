import type { DashboardExternalWorker } from '~/stores/externalAgentInventory'
import { cleanup, fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ExternalAgentProcesses } from './ExternalAgentProcesses'

afterEach(cleanup)

const workers: DashboardExternalWorker[] = [
  {
    workerId: 'windows',
    workerName: 'Windows PC',
    status: 'available',
    observedAt: '2026-10-27T12:30:00Z',
    processes: [{ pid: 123, providerName: 'Claude', processName: 'claude.exe', kind: 'cli', sessions: [] }],
  },
  {
    workerId: 'mac',
    workerName: 'Mac Studio',
    status: 'available',
    processes: [{ pid: 456, providerName: 'Codex', processName: 'Codex', kind: 'desktop', sessions: [] }],
  },
]

function renderProcesses(items: readonly DashboardExternalWorker[] = workers, query = '') {
  const onRefresh = vi.fn()
  render(() => <ExternalAgentProcesses workers={items} query={query} onRefresh={onRefresh} />)
  return { onRefresh }
}

describe('ExternalAgentProcesses', () => {
  it('renders multiple read-only sessions from one process with separate statuses and short identifiers', () => {
    renderProcesses([{
      ...workers[0]!,
      processes: [{
        ...workers[0]!.processes[0]!,
        sessions: [
          { sessionId: 'working-session-123', status: 'working' },
          { sessionId: 'waiting-session-456', status: 'waiting' },
          { sessionId: 'idle-session-789', status: 'idle' },
          { sessionId: 'unknown-session-000', status: 'unknown' },
        ],
      }],
    }])
    const sessions = within(screen.getByRole('list', { name: 'Observed sessions on Windows PC' }))
    expect(sessions.getAllByRole('listitem')).toHaveLength(4)
    expect(sessions.getByText('Working')).toBeInTheDocument()
    expect(sessions.getByText('Waiting on you')).toBeInTheDocument()
    expect(sessions.getByText('Idle')).toBeInTheDocument()
    expect(sessions.getByText('Status unavailable')).toBeInTheDocument()
    expect(sessions.getByText('Session working-sess… · PID 123')).toBeInTheDocument()
    expect(sessions.queryByRole('button')).not.toBeInTheDocument()
    expect(screen.queryByText(/Processes without session observations \(/)).not.toBeInTheDocument()
  })

  it('combines per-session status and identifier search while hiding unobserved helpers under status filters', () => {
    const [filter, setFilter] = createSignal<'all' | 'working' | 'idle'>('all')
    const [query, setQuery] = createSignal('')
    render(() => (
      <ExternalAgentProcesses
        workers={[{
          ...workers[0]!,
          processes: [
            { ...workers[0]!.processes[0]!, sessions: [
              { sessionId: 'one-active-session', status: 'working' },
              { sessionId: 'two-idle-session', status: 'idle' },
            ] },
            { ...workers[0]!.processes[0]!, pid: 456, sessions: [] },
          ],
        }]}
        statusFilter={filter()}
        query={query()}
        onRefresh={undefined}
      />
    ))
    expect(screen.getByText('Processes without session observations (1)').closest('details')).not.toHaveAttribute('open')
    setFilter('working')
    expect(screen.getByText('Working')).toBeInTheDocument()
    expect(screen.queryByText('Idle')).not.toBeInTheDocument()
    expect(screen.queryByText('Processes without session observations (1)')).not.toBeInTheDocument()
    setQuery('two-idle')
    expect(screen.getByText('No matching external sessions, processes, or computers.')).toBeInTheDocument()
    setFilter('idle')
    expect(screen.getByText('Idle')).toBeInTheDocument()
    expect(screen.queryByText('Working')).not.toBeInTheDocument()
  })

  it.each(['loading', 'unavailable', 'offline'] as const)('hides stale session statuses when the observation is %s', (status) => {
    renderProcesses([{
      ...workers[0]!,
      status,
      processes: [{ ...workers[0]!.processes[0]!, sessions: [{ sessionId: 'stale-idle', status: 'idle' }] }],
    }])
    expect(screen.queryByRole('list', { name: 'Observed sessions on Windows PC' })).not.toBeInTheDocument()
    expect(screen.queryByText('Idle', { exact: true })).not.toBeInTheDocument()
  })

  it('shows cli and desktop processes across computers without inferring chat activity or exposing process controls', () => {
    renderProcesses()
    expect(screen.getByRole('region', { name: 'Outside LeapMux' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Windows PC' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Mac Studio' })).toBeInTheDocument()
    for (const summary of screen.getAllByText('Processes without session observations (1)')) {
      expect(summary.closest('details')).not.toHaveAttribute('open')
      fireEvent.click(summary)
    }
    expect(screen.getByText('CLI · claude.exe · PID 123')).toBeInTheDocument()
    expect(screen.getByText('Desktop · Codex · PID 456')).toBeInTheDocument()
    expect(screen.getAllByText('Process detected', { exact: true })).toHaveLength(2)
    expect(screen.getAllByText('Chat activity unknown', { exact: true })).toHaveLength(2)
    expect(screen.queryByText('Working', { exact: true })).not.toBeInTheDocument()
    expect(screen.queryByText('Waiting on you', { exact: true })).not.toBeInTheDocument()
    expect(screen.queryByText('Idle', { exact: true })).not.toBeInTheDocument()
    expect(screen.getAllByRole('button')).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Refresh external processes' })).toBeInTheDocument()
    expect(screen.getByText(/Observed at/).querySelector('time')).toHaveAttribute('datetime', workers[0]!.observedAt)
  })

  it('distinguishes an empty observation, a loading check, unavailable data, and an offline computer', () => {
    renderProcesses([
      { ...workers[0]!, workerId: 'empty', workerName: 'Empty PC', processes: [] },
      { ...workers[0]!, workerId: 'loading', workerName: 'Checking PC', status: 'loading' },
      { ...workers[0]!, workerId: 'failed', workerName: 'Failed PC', status: 'unavailable', statusDetail: 'Update the Worker to support process observations.' },
      { ...workers[0]!, workerId: 'offline', workerName: 'Offline PC', status: 'offline' },
    ])
    expect(screen.getAllByText('No detected agent processes')).toHaveLength(1)
    expect(screen.getByRole('status')).toHaveTextContent('Checking sessions and processes…')
    expect(screen.getByText('Observation unavailable · Session activity unknown')).toBeInTheDocument()
    expect(screen.getByText('Update the Worker to support process observations.')).toBeInTheDocument()
    expect(screen.getByText('Computer offline · Session activity unknown')).toBeInTheDocument()
    expect(screen.queryByText('Process detected', { exact: true })).not.toBeInTheDocument()
    expect(screen.getAllByText(/Observed at/)).toHaveLength(1)
  })

  it('allows an explicit refresh and disables it until loading completes', () => {
    const [liveWorkers, setLiveWorkers] = createSignal<readonly DashboardExternalWorker[]>([
      { ...workers[0]!, status: 'loading' },
    ])
    const onRefresh = vi.fn()
    render(() => <ExternalAgentProcesses workers={liveWorkers()} query="" onRefresh={onRefresh} />)
    const refresh = screen.getByRole('button', { name: 'Refresh external processes' })
    expect(refresh).toBeDisabled()
    setLiveWorkers(workers)
    expect(refresh).toBeEnabled()
    fireEvent.click(refresh)
    expect(onRefresh).toHaveBeenCalledOnce()
  })

  it.each([
    ['  WINDOWS pc ', 'Windows PC'],
    ['claude', 'Windows PC'],
    ['456', 'Mac Studio'],
    ['desktop', 'Mac Studio'],
  ])('searches computer and process metadata for %s', (query, computer) => {
    renderProcesses(workers, query)
    const computers = within(screen.getByRole('list', { name: 'External agent computers' }))
    expect(computers.getAllByRole('heading')).toHaveLength(1)
    expect(computers.getByRole('heading', { name: computer })).toBeInTheDocument()
  })

  it('keeps a successfully observed empty computer visible when its name matches the search', () => {
    renderProcesses([{ ...workers[0]!, workerName: 'Empty PC', processes: [] }], ' empty pc ')
    expect(screen.getByRole('heading', { name: 'Empty PC' })).toBeInTheDocument()
    expect(screen.getByText('No detected agent processes')).toBeInTheDocument()
    expect(screen.queryByText('No matching external sessions, processes, or computers.')).not.toBeInTheDocument()
  })

  it('distinguishes an unmatched search from no detected processes', () => {
    renderProcesses(workers, 'not found')
    expect(screen.getByText('No matching external sessions, processes, or computers.')).toBeInTheDocument()
    expect(screen.queryByText('No detected agent processes')).not.toBeInTheDocument()
  })

  it('states when a response includes only part of the detected processes', () => {
    renderProcesses([{ ...workers[0]!, totalCount: 10 }])
    expect(screen.getByText('Showing 1 of 10 detected processes. Session totals may be incomplete.')).toBeInTheDocument()
  })

  it('does not describe search filtering as a partial scan', () => {
    renderProcesses([{ ...workers[0]!, totalCount: 10 }], 'claude')
    expect(screen.queryByText(/Showing .* detected processes/)).not.toBeInTheDocument()
  })

  it('keeps an unknown process type and a missing observation time explicit', () => {
    renderProcesses([{
      ...workers[0]!,
      observedAt: 'not a date',
      processes: [{ ...workers[0]!.processes[0]!, kind: 'unknown' }],
    }])
    expect(screen.getByText('Process type unknown · claude.exe · PID 123')).toBeInTheDocument()
    expect(screen.queryByText(/Observed at/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Invalid Date/)).not.toBeInTheDocument()
  })

  it('explains when there are no computers to inspect', () => {
    renderProcesses([])
    expect(screen.getByText(/No computers available/)).toBeInTheDocument()
    expect(screen.queryByText('No detected agent processes')).not.toBeInTheDocument()
  })

  it('shows registry loading without suggesting the account has no computers', () => {
    render(() => <ExternalAgentProcesses workers={workers} registryStatus="loading" query="" onRefresh={vi.fn()} />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading computers…')
    expect(screen.queryByText(/No computers available/)).not.toBeInTheDocument()
    expect(screen.queryByText('Process detected', { exact: true })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh external processes' })).toBeDisabled()
  })

  it('hides an unavailable registry and lets refresh retry it despite old loading rows', () => {
    const onRefresh = vi.fn()
    render(() => (
      <ExternalAgentProcesses
        workers={[{ ...workers[0]!, status: 'loading' }]}
        registryStatus="unavailable"
        query=""
        onRefresh={onRefresh}
      />
    ))
    expect(screen.getByRole('status')).toHaveTextContent('Computer list unavailable')
    expect(screen.queryByText('Checking sessions and processes…')).not.toBeInTheDocument()
    expect(screen.queryByText(/No computers available/)).not.toBeInTheDocument()
    const refresh = screen.getByRole('button', { name: 'Refresh external processes' })
    expect(refresh).toBeEnabled()
    fireEvent.click(refresh)
    expect(onRefresh).toHaveBeenCalledOnce()
  })
})
