import type { InventoryWorker } from './useExternalAgentInventory'
import type { InspectNativeAgentProcessesResponse } from '~/generated/proto/leapmux/v1/worker_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { NativeAgentProcessKind, NativeAgentServiceGroupSchema, NativeAgentSessionSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { deferred } from '~/test-support/async'

const inspect = vi.fn<(workerId: string, opts?: { signal?: AbortSignal }) => Promise<InspectNativeAgentProcessesResponse>>()
vi.mock('~/api/workerRpc', () => ({ inspectNativeAgentProcesses: (...args: Parameters<typeof inspect>) => inspect(...args) }))
const { useExternalAgentInventory } = await import('./useExternalAgentInventory')
const disposers: (() => void)[] = []

function response(pid = 100): InspectNativeAgentProcessesResponse {
  return {
    $typeName: 'leapmux.v1.InspectNativeAgentProcessesResponse',
    observedAt: '2026-10-27T12:00:00Z',
    totalCount: 1,
    serviceGroups: [],
    processes: [{
      $typeName: 'leapmux.v1.NativeAgentProcess',
      pid,
      parentPid: 10,
      executableName: 'codex',
      agentProvider: AgentProvider.CODEX,
      kind: NativeAgentProcessKind.UNKNOWN,
      sessions: [],
    }],
  }
}

function setup(open = true, initial: InventoryWorker[] = [{ workerId: 'one', workerName: 'PC one', online: true }]) {
  return createRoot((dispose) => {
    disposers.push(dispose)
    const [enabled, setEnabled] = createSignal(open)
    const [workers, setWorkers] = createSignal(initial)
    return { ...useExternalAgentInventory(workers, enabled), setEnabled, setWorkers }
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  inspect.mockReset()
})
afterEach(() => {
  for (const dispose of disposers.splice(0))
    dispose()
  vi.useRealTimers()
})

describe('useExternalAgentInventory', () => {
  it('retains native provider enums independently of display labels for explicit chat reads', async () => {
    const snapshot = response()
    snapshot.processes[0]!.sessions = [create(NativeAgentSessionSchema, { sessionId: 'process-session' })]
    snapshot.serviceGroups = [create(NativeAgentServiceGroupSchema, {
      sourceId: 'generated-source',
      agentProvider: AgentProvider.CLAUDE_CODE,
      sessions: [create(NativeAgentSessionSchema, { sessionId: 'service-session' })],
    })]
    inspect.mockResolvedValue(snapshot)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.processes[0]?.agentProvider).toBe(AgentProvider.CODEX)
    expect(hook.rows()[0]?.serviceGroups?.[0]?.agentProvider).toBe(AgentProvider.CLAUDE_CODE)
  })

  it('preserves report source and expiry metadata without relabeling a provider observation', async () => {
    const snapshot = response()
    snapshot.processes[0]!.sessions = [
      create(NativeAgentSessionSchema, {
        sessionId: 'reported',
        activityState: AgentActivityState.WAITING_FOR_USER,
        agentReported: true,
        reportedAt: '2026-10-27T12:00:00Z',
        reportExpiresAt: '2026-10-27T12:15:00Z',
      }),
      create(NativeAgentSessionSchema, { sessionId: 'incomplete-report', activityState: AgentActivityState.IDLE, agentReported: true }),
      create(NativeAgentSessionSchema, { sessionId: 'observed', activityState: AgentActivityState.IDLE }),
    ]
    inspect.mockResolvedValue(snapshot)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.processes[0]?.sessions).toEqual([
      { sessionId: 'reported', status: 'waiting', agentReported: true, reportedAt: '2026-10-27T12:00:00Z', reportExpiresAt: '2026-10-27T12:15:00Z' },
      { sessionId: 'incomplete-report', status: 'idle', agentReported: true },
      { sessionId: 'observed', status: 'idle' },
    ])
  })

  it('maps session names and statuses independently and keeps absent or unknown activity unavailable', async () => {
    const snapshot = response()
    snapshot.processes[0]!.sessions = [
      create(NativeAgentSessionSchema, { sessionId: 'working-session', displayName: 'Plan <layout>', activityState: AgentActivityState.WORKING }),
      create(NativeAgentSessionSchema, { sessionId: 'waiting-session', activityState: AgentActivityState.WAITING_FOR_USER }),
      create(NativeAgentSessionSchema, { sessionId: 'idle-session', activityState: AgentActivityState.IDLE }),
      create(NativeAgentSessionSchema, { sessionId: 'missing-state' }),
      create(NativeAgentSessionSchema, { sessionId: 'future-state', activityState: 999 as AgentActivityState }),
    ]
    inspect.mockResolvedValue(snapshot)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.processes[0]?.sessions).toEqual([
      { sessionId: 'working-session', displayName: 'Plan <layout>', status: 'working' },
      { sessionId: 'waiting-session', status: 'waiting' },
      { sessionId: 'idle-session', status: 'idle' },
      { sessionId: 'missing-state', status: 'unknown' },
      { sessionId: 'future-state', status: 'unknown' },
    ])
  })

  it('retains a successful session snapshot while refreshing then clears it on failure', async () => {
    const snapshot = response()
    snapshot.processes[0]!.sessions = [
      create(NativeAgentSessionSchema, { sessionId: 'active-session', activityState: AgentActivityState.WORKING, agentReported: true, reportedAt: '2026-10-27T12:00:00Z', reportExpiresAt: '2026-10-27T12:15:00Z' }),
    ]
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockResolvedValueOnce(snapshot).mockReturnValueOnce(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.processes[0]?.sessions[0]?.status).toBe('working')
    const refresh = hook.refresh()
    expect(hook.rows()[0]).toMatchObject({ status: 'available', refreshing: true, observedAt: snapshot.observedAt })
    expect(hook.rows()[0]?.processes[0]?.sessions[0]?.status).toBe('working')
    pending.reject(new Error('private failure'))
    await refresh
    expect(hook.rows()[0]).toMatchObject({ status: 'unavailable', refreshing: false, processes: [] })
    expect(hook.rows()[0]?.observedAt).toBeUndefined()
    hook.setWorkers([{ workerId: 'one', workerName: 'PC one', online: false }])
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]).toMatchObject({ status: 'offline', processes: [] })
  })

  it('bounds snapshot retention to the poll interval plus request timeout', async () => {
    const snapshot = response()
    snapshot.processes[0]!.sessions = [
      create(NativeAgentSessionSchema, { sessionId: 'known-working', activityState: AgentActivityState.WORKING }),
    ]
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockResolvedValueOnce(snapshot).mockReturnValue(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(15_000)
    expect(inspect).toHaveBeenCalledTimes(2)
    expect(hook.rows()[0]).toMatchObject({ status: 'available', refreshing: true, observedAt: snapshot.observedAt })
    await vi.advanceTimersByTimeAsync(14_999)
    expect(hook.rows()[0]?.processes[0]?.sessions[0]?.status).toBe('working')
    await vi.advanceTimersByTimeAsync(1)
    expect(hook.rows()[0]).toMatchObject({ status: 'unavailable', refreshing: false, processes: [] })
    expect(hook.rows()[0]?.observedAt).toBeUndefined()
    pending.resolve(snapshot)
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.status).toBe('unavailable')
  })

  it('does not extend an unreachable computer snapshot when registry changes restart its request', async () => {
    const snapshot = response()
    snapshot.processes[0]!.sessions = [
      create(NativeAgentSessionSchema, { sessionId: 'previously-working', activityState: AgentActivityState.WORKING }),
    ]
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockResolvedValueOnce(snapshot).mockReturnValue(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    const refresh = hook.refresh()
    await vi.advanceTimersByTimeAsync(5_000)
    hook.setWorkers([{ workerId: 'one', workerName: 'Renamed PC', online: true }])
    await vi.advanceTimersByTimeAsync(5_000)
    hook.setWorkers([
      { workerId: 'one', workerName: 'Renamed PC', online: true },
      { workerId: 'offline', workerName: 'Other PC', online: false },
    ])
    await vi.advanceTimersByTimeAsync(4_999)
    expect(hook.rows()[0]).toMatchObject({ status: 'available', refreshing: true })
    expect(hook.rows()[0]?.processes[0]?.sessions[0]?.status).toBe('working')
    await vi.advanceTimersByTimeAsync(1)
    expect(hook.rows()[0]).toMatchObject({ status: 'unavailable', refreshing: false, processes: [] })
    expect(hook.rows()[0]?.observedAt).toBeUndefined()
    pending.resolve(snapshot)
    await refresh
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.status).toBe('unavailable')
    // Expiry of the retained snapshot does not permanently disable retry.
    await hook.refresh()
    expect(hook.rows()[0]).toMatchObject({ status: 'available', refreshing: false })
  })

  it('retains existing computer snapshots during registry additions and renames without accepting obsolete replies', async () => {
    const obsolete = deferred<InspectNativeAgentProcessesResponse>()
    const current = deferred<InspectNativeAgentProcessesResponse>()
    const added = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockResolvedValueOnce(response(100)).mockReturnValueOnce(obsolete.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    const oldRefresh = hook.refresh()
    const oldSignal = inspect.mock.calls[1]![1]!.signal!
    inspect.mockImplementation(id => id === 'one' ? current.promise : added.promise)
    hook.setWorkers([
      { workerId: 'one', workerName: 'Renamed PC', online: true },
      { workerId: 'two', workerName: 'New PC', online: true },
    ])
    await vi.advanceTimersByTimeAsync(0)
    expect(oldSignal.aborted).toBe(true)
    expect(hook.rows()[0]).toMatchObject({ workerName: 'Renamed PC', status: 'available', refreshing: true, processes: [{ pid: 100 }] })
    expect(hook.rows()[1]).toMatchObject({ workerId: 'two', status: 'loading', processes: [] })
    obsolete.resolve(response(999))
    await oldRefresh
    expect(hook.rows()[0]?.processes[0]?.pid).toBe(100)
    current.resolve(response(200))
    added.resolve(response(300))
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows().map(row => row.processes[0]?.pid)).toEqual([200, 300])
    expect(hook.rows().every(row => row.status === 'available' && !row.refreshing)).toBe(true)
  })

  it('clears a retained snapshot immediately when the computer identity changes', async () => {
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockResolvedValueOnce(response()).mockReturnValue(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    const refresh = hook.refresh()
    expect(hook.rows()[0]?.refreshing).toBe(true)
    hook.setWorkers([{ workerId: 'replacement', workerName: 'New PC', online: true }])
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]).toMatchObject({ workerId: 'replacement', status: 'loading', processes: [] })
    expect(hook.rows()[0]?.observedAt).toBeUndefined()
    expect(hook.rows()[0]?.refreshing).not.toBe(true)
    await refresh
  })

  it('observes only while open and refreshes without asserting chat activity', async () => {
    inspect.mockResolvedValue(response())
    const hook = setup(false)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(inspect).not.toHaveBeenCalled()
    hook.setEnabled(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(inspect).toHaveBeenCalledTimes(1)
    expect(hook.rows()[0]).toMatchObject({ status: 'available', observedAt: '2026-10-27T12:00:00Z', processes: [{ pid: 100, kind: 'unknown' }] })
    await vi.advanceTimersByTimeAsync(15_000)
    expect(inspect).toHaveBeenCalledTimes(2)
    hook.setEnabled(false)
    await vi.advanceTimersByTimeAsync(15_000)
    expect(hook.rows()).toEqual([])
    expect(inspect).toHaveBeenCalledTimes(2)
  })

  it('keeps offline, failed, and empty observations distinct across PCs', async () => {
    inspect.mockImplementation(async (id) => {
      if (id === 'failed')
        throw new Error('private diagnostic must not reach UI')
      return { ...response(), processes: [], totalCount: 0 }
    })
    const hook = setup(true, [
      { workerId: 'offline', workerName: 'Offline PC', online: false },
      { workerId: 'failed', workerName: 'Failed PC', online: true },
      { workerId: 'empty', workerName: 'Empty PC', online: true },
    ])
    await vi.advanceTimersByTimeAsync(0)
    expect(inspect).toHaveBeenCalledTimes(2)
    expect(hook.rows().map(row => row.status)).toEqual(['offline', 'unavailable', 'available'])
    expect(JSON.stringify(hook.rows())).not.toContain('private diagnostic')
  })

  it('discards late observations after disconnection and refetches on reconnect', async () => {
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockReturnValueOnce(pending.promise).mockResolvedValue(response(200))
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    const signal = inspect.mock.calls[0]![1]!.signal!
    hook.setWorkers([{ workerId: 'one', workerName: 'PC one', online: false }])
    await vi.advanceTimersByTimeAsync(0)
    expect(signal.aborted).toBe(true)
    pending.resolve(response())
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]).toMatchObject({ status: 'offline', processes: [] })
    hook.setWorkers([{ workerId: 'one', workerName: 'PC one', online: true }])
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.processes[0]?.pid).toBe(200)
  })

  it('does not overlap requests or refetch for equivalent Worker values', async () => {
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockReturnValue(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    hook.setWorkers([{ workerId: 'one', workerName: 'PC one', online: true }])
    await hook.refresh()
    expect(inspect).toHaveBeenCalledTimes(1)
    pending.resolve(response())
    await vi.advanceTimersByTimeAsync(0)
    await hook.refresh()
    expect(inspect).toHaveBeenCalledTimes(2)
  })

  it('aborts a stalled request and reports unavailable rather than no processes', async () => {
    inspect.mockImplementation((_id, opts) => new Promise((_resolve, reject) => {
      opts?.signal?.addEventListener('abort', () => reject(new Error('aborted')), { once: true })
    }))
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.status).toBe('loading')
    await vi.advanceTimersByTimeAsync(15_000)
    expect(hook.rows()[0]).toMatchObject({ status: 'unavailable', refreshing: false, processes: [] })
  })

  it('bounds a stalled channel handshake even when it ignores abort', async () => {
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockReturnValue(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(15_000)
    expect(hook.rows()[0]?.status).toBe('unavailable')
    pending.resolve(response())
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]).toMatchObject({ status: 'unavailable', refreshing: false, processes: [] })
  })

  it('aborts and ignores a late response after the dashboard closes', async () => {
    const pending = deferred<InspectNativeAgentProcessesResponse>()
    inspect.mockReturnValue(pending.promise)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    const signal = inspect.mock.calls[0]![1]!.signal!
    hook.setEnabled(false)
    await vi.advanceTimersByTimeAsync(0)
    pending.resolve(response())
    await vi.advanceTimersByTimeAsync(0)
    expect(signal.aborted).toBe(true)
    expect(hook.rows()).toEqual([])
  })
})

describe('useExternalAgentInventory service groups', () => {
  it('maps generic service coverage and original evidence timestamps then clears them on failure', async () => {
    const snapshot = response()
    snapshot.serviceGroups = [create(NativeAgentServiceGroupSchema, {
      sourceId: 'application-service',
      agentProvider: AgentProvider.CODEX,
      servicePid: 800,
      executableName: 'codex',
      available: true,
      partial: true,
      candidateCount: 128,
      candidateTruncated: true,
      subscriptionLimit: 8,
      sessions: [{
        sessionId: 'native-chat',
        displayName: 'Native plan',
        activityState: AgentActivityState.WORKING,
        statusObservedAt: '2026-10-27T12:00:00Z',
        sourceVerifiedAt: '2026-10-27T12:00:20Z',
        expiresAt: '2026-10-27T12:01:05Z',
      }],
    })]
    inspect.mockResolvedValueOnce(snapshot).mockRejectedValueOnce(new Error('source unavailable'))
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]?.serviceGroups).toEqual([{
      sourceId: 'application-service',
      agentProvider: AgentProvider.CODEX,
      providerName: 'Codex',
      servicePid: 800,
      processName: 'codex',
      available: true,
      partial: true,
      candidateCount: 128,
      candidateTruncated: true,
      subscriptionLimit: 8,
      sessions: [{ sessionId: 'native-chat', displayName: 'Native plan', status: 'working', statusObservedAt: '2026-10-27T12:00:00Z', sourceVerifiedAt: '2026-10-27T12:00:20Z', expiresAt: '2026-10-27T12:01:05Z' }],
    }])
    await hook.refresh()
    expect(hook.rows()[0]).toMatchObject({ status: 'unavailable', serviceGroups: [], processes: [] })
  })

  it('normalizes absent repeated response fields without inventing unavailable sessions', async () => {
    const snapshot = response()
    Object.assign(snapshot, { processes: undefined, serviceGroups: undefined })
    inspect.mockResolvedValue(snapshot)
    const hook = setup()
    await vi.advanceTimersByTimeAsync(0)
    expect(hook.rows()[0]).toMatchObject({ status: 'available', processes: [], serviceGroups: [] })
  })
})
