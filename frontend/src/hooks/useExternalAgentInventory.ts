import type { Accessor } from 'solid-js'
import type { NativeAgentSession } from '~/generated/proto/leapmux/v1/worker_pb'
import type { DashboardExternalSession, DashboardExternalWorker } from '~/stores/externalAgentInventory'
import { createEffect, createMemo, createSignal, on, onCleanup, untrack } from 'solid-js'
import { inspectNativeAgentProcesses } from '~/api/workerRpc'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'
import { NativeAgentProcessKind } from '~/generated/proto/leapmux/v1/worker_pb'

export interface InventoryWorker {
  workerId: string
  workerName: string
  online: boolean
}

const REFRESH_INTERVAL_MS = 15_000
const REQUEST_TIMEOUT_MS = 15_000

function sessionStatus(state: AgentActivityState): DashboardExternalSession['status'] {
  switch (state) {
    case AgentActivityState.WORKING:
      return 'working'
    case AgentActivityState.WAITING_FOR_USER:
      return 'waiting'
    case AgentActivityState.IDLE:
      return 'idle'
    default:
      return 'unknown'
  }
}

function mapSession(session: NativeAgentSession): DashboardExternalSession {
  return {
    sessionId: session.sessionId,
    ...(session.displayName ? { displayName: session.displayName } : {}),
    ...(session.agentReported ? { agentReported: true } : {}),
    ...(session.reportedAt ? { reportedAt: session.reportedAt } : {}),
    ...(session.reportExpiresAt ? { reportExpiresAt: session.reportExpiresAt } : {}),
    ...(session.statusObservedAt ? { statusObservedAt: session.statusObservedAt } : {}),
    ...(session.sourceVerifiedAt ? { sourceVerifiedAt: session.sourceVerifiedAt } : {}),
    ...(session.expiresAt ? { expiresAt: session.expiresAt } : {}),
    status: sessionStatus(session.activityState),
  }
}
/** Observe only while the dashboard is open. A failed observation is never an empty inventory. */
export function useExternalAgentInventory(
  workers: Accessor<readonly InventoryWorker[]>,
  enabled: Accessor<boolean>,
) {
  const [rows, setRows] = createSignal<DashboardExternalWorker[]>([])
  let generation = 0
  let pending = false
  let controllers: AbortController[] = []
  // Registry changes may restart a request, but must not renew the lifetime of
  // its retained snapshot (including providers without per-session expiry).
  const observationDeadlines = new Map<string, number>()
  const cancel = () => {
    generation++
    for (const controller of controllers)
      controller.abort()
    controllers = []
    pending = false
  }

  // Compare values so unrelated Worker metadata updates do not restart scans.
  const source = createMemo(() => enabled() ? workers().map(w => ({ ...w })) : null, undefined, {
    equals: (a, b) => JSON.stringify(a) === JSON.stringify(b),
  })

  const refresh = async (): Promise<void> => {
    const current = untrack(source)
    if (current === null || pending)
      return
    cancel()
    const run = generation
    pending = true
    const onlineIds = new Set(current.filter(worker => worker.online).map(worker => worker.workerId))
    for (const workerId of observationDeadlines.keys()) {
      if (!onlineIds.has(workerId))
        observationDeadlines.delete(workerId)
    }
    for (const workerId of onlineIds) {
      if (!observationDeadlines.has(workerId))
        observationDeadlines.set(workerId, Date.now() + REQUEST_TIMEOUT_MS)
    }
    setRows((previous) => {
      const byId = new Map(previous.map(row => [row.workerId, row]))
      return current.map((worker): DashboardExternalWorker => {
        const snapshot = byId.get(worker.workerId)
        // Preserve a successful snapshot for this same computer during the
        // bounded request, including registry additions and display-name edits.
        if (worker.online && snapshot?.status === 'available')
          return { ...snapshot, workerName: worker.workerName, refreshing: true }
        return {
          workerId: worker.workerId,
          workerName: worker.workerName,
          status: worker.online ? 'loading' : 'offline',
          processes: [],
        }
      })
    })
    await Promise.all(current.filter(w => w.online).map(async (worker) => {
      const controller = new AbortController()
      controllers.push(controller)
      const remaining = Math.max(0, observationDeadlines.get(worker.workerId)! - Date.now())
      const timeout = setTimeout(() => controller.abort(), remaining)
      let rejectAborted: (() => void) | undefined
      let observation: Partial<DashboardExternalWorker>
      try {
        const aborted = new Promise<never>((_resolve, reject) => {
          rejectAborted = () => reject(new Error('Observation cancelled'))
          controller.signal.addEventListener('abort', rejectAborted, { once: true })
        })
        // Channel creation may outlive the RPC signal. Bound this observation
        // independently so one unreachable computer cannot hold Refresh forever.
        const response = await Promise.race([
          inspectNativeAgentProcesses(worker.workerId, { signal: controller.signal }),
          aborted,
        ])
        observation = {
          status: 'available',
          refreshing: false,
          observedAt: response.observedAt || undefined,
          statusDetail: undefined,
          totalCount: response.totalCount,
          processes: (response.processes ?? []).map(process => ({
            pid: process.pid,
            processName: process.executableName,
            providerName: agentProviderLabel(process.agentProvider),
            kind: process.kind === NativeAgentProcessKind.CLI ? 'cli' : process.kind === NativeAgentProcessKind.DESKTOP ? 'desktop' : 'unknown',
            sessions: (process.sessions ?? []).map(mapSession),
          })),
          serviceGroups: (response.serviceGroups ?? []).map(group => ({
            sourceId: group.sourceId,
            providerName: agentProviderLabel(group.agentProvider),
            servicePid: group.servicePid,
            processName: group.executableName,
            available: group.available,
            partial: group.partial,
            candidateCount: group.candidateCount,
            candidateTruncated: group.candidateTruncated,
            subscriptionLimit: group.subscriptionLimit,
            sessions: (group.sessions ?? []).map(mapSession),
          })),
        }
      }
      catch {
        observation = {
          status: 'unavailable',
          refreshing: false,
          processes: [],
          serviceGroups: [],
          observedAt: undefined,
          totalCount: undefined,
          statusDetail: 'This computer could not provide an observation. Check its connection and Worker version, then refresh.',
        }
      }
      finally {
        clearTimeout(timeout)
        if (rejectAborted)
          controller.signal.removeEventListener('abort', rejectAborted)
      }
      // Closing the dashboard, disconnecting a Worker, or starting a new scan
      // invalidates replies already in flight, even if the transport ignored abort.
      if (generation !== run)
        return
      observationDeadlines.delete(worker.workerId)
      setRows(previous => previous.map(row => row.workerId === worker.workerId ? { ...row, ...observation } : row))
    }))
    if (generation === run) {
      pending = false
      controllers = []
    }
  }

  createEffect(on(source, (current) => {
    cancel()
    if (current === null) {
      observationDeadlines.clear()
      setRows([])
    }
    else {
      void refresh()
    }
  }))
  const interval = setInterval(() => {
    void refresh()
  }, REFRESH_INTERVAL_MS)
  onCleanup(() => {
    clearInterval(interval)
    cancel()
    observationDeadlines.clear()
  })
  return { rows, refresh }
}
