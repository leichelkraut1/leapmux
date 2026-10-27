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
    processes: [{ pid: 123, providerName: 'Claude', processName: 'claude.exe', kind: 'cli' }],
  },
  {
    workerId: 'mac',
    workerName: 'Mac Studio',
    status: 'available',
    processes: [{ pid: 456, providerName: 'Codex', processName: 'Codex', kind: 'desktop' }],
  },
]

function renderProcesses(items: readonly DashboardExternalWorker[] = workers, query = '') {
  const onRefresh = vi.fn()
  render(() => <ExternalAgentProcesses workers={items} query={query} onRefresh={onRefresh} />)
  return { onRefresh }
}

describe('ExternalAgentProcesses', () => {
  it('shows cli and desktop processes across computers without inferring chat activity or exposing process controls', () => {
    renderProcesses()
    expect(screen.getByRole('region', { name: 'Outside LeapMux' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Windows PC' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Mac Studio' })).toBeInTheDocument()
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
    expect(screen.getByRole('status')).toHaveTextContent('Checking processes…')
    expect(screen.getByText('Observation unavailable')).toBeInTheDocument()
    expect(screen.getByText('Update the Worker to support process observations.')).toBeInTheDocument()
    expect(screen.getByText('Computer offline')).toBeInTheDocument()
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

  it('distinguishes an unmatched search from no detected processes', () => {
    renderProcesses(workers, 'not found')
    expect(screen.getByText('No matching external processes or computers.')).toBeInTheDocument()
    expect(screen.queryByText('No detected agent processes')).not.toBeInTheDocument()
  })

  it('states when a response includes only part of the detected processes', () => {
    renderProcesses([{ ...workers[0]!, totalCount: 10 }])
    expect(screen.getByText('Showing 1 of 10 detected processes.')).toBeInTheDocument()
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
    expect(screen.queryByText('Checking processes…')).not.toBeInTheDocument()
    expect(screen.queryByText(/No computers available/)).not.toBeInTheDocument()
    const refresh = screen.getByRole('button', { name: 'Refresh external processes' })
    expect(refresh).toBeEnabled()
    fireEvent.click(refresh)
    expect(onRefresh).toHaveBeenCalledOnce()
  })
})
