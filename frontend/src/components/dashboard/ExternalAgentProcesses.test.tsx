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
  render(() => <ExternalAgentProcesses nowMs={Date.now()} workers={items} query={query} onRefresh={onRefresh} />)
  return { onRefresh }
}

describe('ExternalAgentProcesses', () => {
  it('preserves an expanded helper list across report-clock ticks', () => {
    const [now, setNow] = createSignal(Date.parse('2026-10-27T12:01:00Z'))
    render(() => (
      <ExternalAgentProcesses
        nowMs={now()}
        workers={workers}
        query=""
        onRefresh={undefined}
      />
    ))
    const details = screen.getAllByText('Processes without session status (1)')[0]!.closest('details')!
    details.open = true
    setNow(Date.parse('2026-10-27T12:01:01Z'))
    expect(details).toBeInTheDocument()
    expect(details).toHaveAttribute('open')
  })

  it('labels participating-agent reports separately from provider observations and shows their deadline', () => {
    const reportedAt = '2026-10-27T12:00:00Z'
    const reportExpiresAt = '2026-10-27T12:15:00Z'
    render(() => (
      <ExternalAgentProcesses
        nowMs={Date.parse('2026-10-27T12:10:00Z')}
        workers={[{
          ...workers[0]!,
          processes: [{
            ...workers[0]!.processes[0]!,
            sessions: [
              { sessionId: 'observed', status: 'idle' },
              { sessionId: 'reported', displayName: 'Reported plan', status: 'waiting', agentReported: true, reportedAt, reportExpiresAt },
            ],
          }],
        }]}
        query=""
        onRefresh={undefined}
      />
    ))
    const sessions = within(screen.getByRole('list', { name: 'External sessions on Windows PC' }))
    expect(sessions.getAllByText('Read-only observation')).toHaveLength(1)
    const reported = sessions.getByText('Reported plan').closest('li')!
    expect(within(reported).getByText('Waiting on you')).toBeInTheDocument()
    expect(within(reported).getByText(/Agent-reported · Last reported/).querySelector('time')).toHaveAttribute('datetime', reportedAt)
    expect(within(reported).getByText(/Fresh report · Expires/).querySelector('time')).toHaveAttribute('datetime', reportExpiresAt)
    expect(within(reported).queryByText('Read-only observation')).not.toBeInTheDocument()
    expect(within(reported).queryByRole('button')).not.toBeInTheDocument()
  })

  it('keeps reports with missing timestamps visible as unavailable without implying fresh activity', () => {
    renderProcesses([{
      ...workers[0]!,
      processes: [{
        ...workers[0]!.processes[0]!,
        sessions: [{ sessionId: 'missing-timestamps', status: 'working', agentReported: true }],
      }],
    }])
    const sessions = within(screen.getByRole('list', { name: 'External sessions on Windows PC' }))
    expect(sessions.getByText('Status unavailable')).toBeInTheDocument()
    expect(sessions.getByText(/Agent-reported · Last reported/)).toHaveTextContent('time unavailable')
    expect(sessions.getByText('Report freshness unavailable · Activity unknown')).toBeInTheDocument()
    expect(sessions.queryByText('Working')).not.toBeInTheDocument()
    expect(sessions.queryByText(/Invalid Date/)).not.toBeInTheDocument()
  })

  it('renders the native name as literal text while preserving provider and session identity', () => {
    const name = '<img src=x onerror="alert(1)"> & rollout'
    renderProcesses([{
      ...workers[0]!,
      processes: [{
        ...workers[0]!.processes[0]!,
        sessions: [{ sessionId: 'named-id', displayName: name, status: 'working' }],
      }],
    }])
    const sessions = screen.getByRole('list', { name: 'External sessions on Windows PC' })
    expect(within(sessions).getByText(name).tagName).toBe('STRONG')
    expect(within(sessions).getByText('Claude · Session named-id · PID 123')).toBeInTheDocument()
    expect(sessions.querySelector('img')).toBeNull()
    expect(within(sessions).queryByRole('button')).not.toBeInTheDocument()
  })

  it.each([undefined, '', '   '])('falls back to the provider when the native name is %s', (displayName) => {
    renderProcesses([{
      ...workers[0]!,
      processes: [{
        ...workers[0]!.processes[0]!,
        sessions: [{
          sessionId: 'unnamed-id',
          ...(displayName === undefined ? {} : { displayName }),
          status: 'idle',
        }],
      }],
    }])
    const sessions = within(screen.getByRole('list', { name: 'External sessions on Windows PC' }))
    expect(sessions.getByText('Claude', { exact: true }).tagName).toBe('STRONG')
    expect(sessions.getByText('Session unnamed-id · PID 123')).toBeInTheDocument()
    expect(sessions.getByText('Idle')).toBeInTheDocument()
  })

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
    const sessions = within(screen.getByRole('list', { name: 'External sessions on Windows PC' }))
    expect(sessions.getAllByRole('listitem')).toHaveLength(4)
    expect(sessions.getByText('Working')).toBeInTheDocument()
    expect(sessions.getByText('Waiting on you')).toBeInTheDocument()
    expect(sessions.getByText('Idle')).toBeInTheDocument()
    expect(sessions.getByText('Status unavailable')).toBeInTheDocument()
    expect(sessions.getByText('Session working-sess… · PID 123')).toBeInTheDocument()
    expect(sessions.queryByRole('button')).not.toBeInTheDocument()
    expect(screen.queryByText(/Processes without session status \(/)).not.toBeInTheDocument()
  })

  it('combines per-session status and identifier search while hiding unobserved helpers under status filters', () => {
    const [filter, setFilter] = createSignal<'all' | 'working' | 'idle'>('all')
    const [query, setQuery] = createSignal('')
    render(() => (
      <ExternalAgentProcesses
        nowMs={Date.now()}
        workers={[{
          ...workers[0]!,
          processes: [
            { ...workers[0]!.processes[0]!, sessions: [
              { sessionId: 'one-active-session', status: 'working' },
              { sessionId: 'two-idle-session', displayName: 'Release checklist', status: 'idle' },
            ] },
            { ...workers[0]!.processes[0]!, pid: 456, sessions: [] },
          ],
        }]}
        statusFilter={filter()}
        query={query()}
        onRefresh={undefined}
      />
    ))
    expect(screen.getByText('Processes without session status (1)').closest('details')).not.toHaveAttribute('open')
    setFilter('working')
    expect(screen.getByText('Working')).toBeInTheDocument()
    expect(screen.queryByText('Idle')).not.toBeInTheDocument()
    expect(screen.queryByText('Processes without session status (1)')).not.toBeInTheDocument()
    setQuery('two-idle')
    expect(screen.getByText('No matching external sessions, processes, or computers.')).toBeInTheDocument()
    setFilter('idle')
    expect(screen.getByText('Idle')).toBeInTheDocument()
    expect(screen.queryByText('Working')).not.toBeInTheDocument()
    setQuery(' RELEASE CHECKLIST ')
    expect(screen.getByText('Release checklist')).toBeInTheDocument()
    expect(screen.getByText('Idle')).toBeInTheDocument()
    setFilter('working')
    expect(screen.getByText('No matching external sessions, processes, or computers.')).toBeInTheDocument()
  })

  it.each(['loading', 'unavailable', 'offline'] as const)('hides stale session statuses when the observation is %s', (status) => {
    renderProcesses([{
      ...workers[0]!,
      status,
      processes: [{ ...workers[0]!.processes[0]!, sessions: [{ sessionId: 'stale-idle', status: 'idle' }] }],
    }])
    expect(screen.queryByRole('list', { name: 'External sessions on Windows PC' })).not.toBeInTheDocument()
    expect(screen.queryByText('Idle', { exact: true })).not.toBeInTheDocument()
  })

  it('shows cli and desktop processes across computers without inferring chat activity or exposing process controls', () => {
    renderProcesses()
    expect(screen.getByRole('region', { name: 'Outside LeapMux' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Windows PC' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Mac Studio' })).toBeInTheDocument()
    for (const summary of screen.getAllByText('Processes without session status (1)')) {
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
    expect(screen.getByText(/Checked at/).querySelector('time')).toHaveAttribute('datetime', workers[0]!.observedAt)
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
    expect(screen.getAllByText(/Checked at/)).toHaveLength(1)
  })

  it('allows an explicit refresh and disables it until loading completes', () => {
    const [liveWorkers, setLiveWorkers] = createSignal<readonly DashboardExternalWorker[]>([
      { ...workers[0]!, status: 'loading' },
    ])
    const onRefresh = vi.fn()
    render(() => <ExternalAgentProcesses nowMs={Date.now()} workers={liveWorkers()} query="" onRefresh={onRefresh} />)
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
    expect(screen.queryByText(/Checked at/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Invalid Date/)).not.toBeInTheDocument()
  })

  it('explains when there are no computers to inspect', () => {
    renderProcesses([])
    expect(screen.getByText(/No computers available/)).toBeInTheDocument()
    expect(screen.queryByText('No detected agent processes')).not.toBeInTheDocument()
  })

  it('shows registry loading without suggesting the account has no computers', () => {
    render(() => <ExternalAgentProcesses nowMs={Date.now()} workers={workers} registryStatus="loading" query="" onRefresh={vi.fn()} />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading computers…')
    expect(screen.queryByText(/No computers available/)).not.toBeInTheDocument()
    expect(screen.queryByText('Process detected', { exact: true })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh external processes' })).toBeDisabled()
  })

  it('hides an unavailable registry and lets refresh retry it despite old loading rows', () => {
    const onRefresh = vi.fn()
    render(() => (
      <ExternalAgentProcesses
        nowMs={Date.now()}
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
