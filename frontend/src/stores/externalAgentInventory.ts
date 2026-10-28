export type WorkerRegistryStatus = 'loading' | 'available' | 'unavailable'
export type DashboardStatusFilter = 'all' | 'working' | 'waiting' | 'idle'

export interface DashboardExternalSession {
  sessionId: string
  /** Provider-supplied or agent-reported name, normalized and bounded by the Worker. */
  displayName?: string
  status: 'working' | 'waiting' | 'idle' | 'unknown'
  /** Participating-agent reports are distinct from provider observations. */
  agentReported?: boolean
  reportedAt?: string
  reportExpiresAt?: string
}

/** Process presence alone never determines session activity. */
export interface DashboardExternalProcess {
  pid: number
  providerName: string
  processName: string
  kind: 'cli' | 'desktop' | 'unknown'
  sessions: readonly DashboardExternalSession[]
}

export interface DashboardExternalWorker {
  workerId: string
  workerName: string
  status: 'loading' | 'available' | 'unavailable' | 'offline'
  /** The previous snapshot remains visible only during a bounded refresh. */
  refreshing?: boolean
  processes: readonly DashboardExternalProcess[]
  /** ISO timestamp of the observation, not the last chat activity. */
  observedAt?: string | undefined
  totalCount?: number | undefined
  statusDetail?: string | undefined
}

/** The Worker supplies the bounded expiry; no client-side lifetime is inferred. */
export function externalSessionReportFreshness(
  session: DashboardExternalSession,
  nowMs: number,
): 'fresh' | 'expired' | 'unknown' {
  const reportedAt = Date.parse(session.reportedAt ?? '')
  const expiresAt = Date.parse(session.reportExpiresAt ?? '')
  if (!Number.isFinite(nowMs) || !Number.isFinite(reportedAt) || !Number.isFinite(expiresAt)
    || reportedAt > nowMs || expiresAt <= reportedAt) {
    return 'unknown'
  }
  return nowMs >= expiresAt ? 'expired' : 'fresh'
}

export function resolveExternalSessionStatus(
  session: DashboardExternalSession,
  nowMs: number,
): DashboardExternalSession['status'] {
  return session.agentReported && externalSessionReportFreshness(session, nowMs) !== 'fresh'
    ? 'unknown'
    : session.status
}

/** Count sessions, never native processes or desktop helpers. */
export function externalAgentSessions(workers: readonly DashboardExternalWorker[], nowMs = Date.now()): DashboardExternalSession[] {
  return workers.flatMap(worker => worker.status === 'available'
    ? worker.processes.flatMap(process => process.sessions.map(session => ({ ...session, status: resolveExternalSessionStatus(session, nowMs) })))
    : [])
}

/** Apply the same search and resolved status selection to each external session. */
export function filterExternalAgentWorkers(
  workers: readonly DashboardExternalWorker[],
  query: string,
  status: DashboardStatusFilter,
  nowMs = Date.now(),
): DashboardExternalWorker[] {
  const search = query.trim().toLocaleLowerCase()
  return workers.flatMap((worker) => {
    const workerMatches = !search || worker.workerName.toLocaleLowerCase().includes(search)
    if (worker.status !== 'available')
      return workerMatches ? [worker] : []
    const processes = worker.processes.flatMap((process) => {
      const processMatches = workerMatches || [process.providerName, process.processName, process.kind, String(process.pid)]
        .some(value => value.toLocaleLowerCase().includes(search))
      if (process.sessions.length === 0)
        return status === 'all' && processMatches ? [process] : []
      const sessions = process.sessions.filter(session =>
        (status === 'all' || resolveExternalSessionStatus(session, nowMs) === status)
        && (processMatches || [session.sessionId, session.displayName ?? '']
          .some(value => value.toLocaleLowerCase().includes(search))),
      )
      return sessions.length > 0 ? [{ ...process, sessions }] : []
    })
    return processes.length > 0 || (workerMatches && status === 'all') ? [{ ...worker, processes }] : []
  })
}
