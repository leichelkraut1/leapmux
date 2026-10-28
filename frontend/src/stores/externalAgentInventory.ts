export type WorkerRegistryStatus = 'loading' | 'available' | 'unavailable'
export type DashboardStatusFilter = 'all' | 'working' | 'waiting' | 'idle'

export interface DashboardExternalSession {
  sessionId: string
  /** Provider-supplied or agent-reported name, normalized and bounded by the Worker. */
  displayName?: string
  status: 'working' | 'waiting' | 'idle' | 'unknown'
  agentReported?: boolean
  reportedAt?: string
  reportExpiresAt?: string
  /** Runtime evidence and source heartbeat are distinct from a new activity event. */
  runtimeObserved?: boolean
  sourceAvailable?: boolean
  statusObservedAt?: string
  sourceVerifiedAt?: string
  expiresAt?: string
}

/** Process presence alone never determines session activity. */
export interface DashboardExternalProcess {
  pid: number
  providerName: string
  processName: string
  kind: 'cli' | 'desktop' | 'unknown'
  sessions: readonly DashboardExternalSession[]
}

/** An application service can observe many sessions; its PID is not an agent PID. */
export interface DashboardExternalServiceGroup {
  sourceId: string
  providerName: string
  servicePid: number
  processName: string
  available: boolean
  partial: boolean
  candidateCount: number
  candidateTruncated: boolean
  subscriptionLimit: number
  sessions: readonly DashboardExternalSession[]
}

export interface DashboardExternalWorker {
  workerId: string
  workerName: string
  status: 'loading' | 'available' | 'unavailable' | 'offline'
  /** The previous snapshot remains visible only during a bounded refresh. */
  refreshing?: boolean
  processes: readonly DashboardExternalProcess[]
  serviceGroups?: readonly DashboardExternalServiceGroup[]
  /** ISO timestamp of the observation, not the last chat activity. */
  observedAt?: string | undefined
  totalCount?: number | undefined
  /** Retain the capture size when duplicate process rows are hidden. */
  observedProcessCount?: number
  statusDetail?: string | undefined
}

type Freshness = 'fresh' | 'expired' | 'unknown'

/** The Worker supplies the bounded expiry; no client-side lifetime is inferred. */
export function externalSessionReportFreshness(session: DashboardExternalSession, nowMs: number): Freshness {
  const reportedAt = Date.parse(session.reportedAt ?? '')
  const expiresAt = Date.parse(session.reportExpiresAt ?? '')
  if (!Number.isFinite(nowMs) || !Number.isFinite(reportedAt) || !Number.isFinite(expiresAt)
    || reportedAt > nowMs || expiresAt <= reportedAt) {
    return 'unknown'
  }
  return nowMs >= expiresAt ? 'expired' : 'fresh'
}

export function externalSessionObservationFreshness(session: DashboardExternalSession, nowMs: number): Freshness {
  const observedAt = Date.parse(session.statusObservedAt ?? '')
  const verifiedAt = Date.parse(session.sourceVerifiedAt ?? '')
  const expiresAt = Date.parse(session.expiresAt ?? '')
  if (session.sourceAvailable === false || !Number.isFinite(nowMs) || !Number.isFinite(observedAt)
    || !Number.isFinite(verifiedAt) || !Number.isFinite(expiresAt) || observedAt > verifiedAt
    || verifiedAt > nowMs || expiresAt <= verifiedAt) {
    return 'unknown'
  }
  return nowMs >= expiresAt ? 'expired' : 'fresh'
}

export function resolveExternalSessionStatus(session: DashboardExternalSession, nowMs: number): DashboardExternalSession['status'] {
  if (session.runtimeObserved && externalSessionObservationFreshness(session, nowMs) !== 'fresh')
    return 'unknown'
  if (session.agentReported && externalSessionReportFreshness(session, nowMs) !== 'fresh')
    return 'unknown'
  return session.status
}

function sessionRank(session: DashboardExternalSession, nowMs: number): number {
  if (session.runtimeObserved)
    return externalSessionObservationFreshness(session, nowMs) === 'fresh' ? 0 : 3
  if (!session.agentReported)
    return 1
  return externalSessionReportFreshness(session, nowMs) === 'fresh' ? 2 : 4
}

/** Prefer fresh runtime evidence over a duplicate agent claim, scoped to one PC and provider. */
export function projectExternalWorker(worker: DashboardExternalWorker, nowMs: number): DashboardExternalWorker {
  const groups = (worker.serviceGroups ?? []).map(group => ({
    ...group,
    sessions: group.sessions.map(session => ({ ...session, runtimeObserved: true, sourceAvailable: group.available })),
  }))
  const selected = new Map<string, DashboardExternalSession>()
  const key = (provider: string, session: DashboardExternalSession) => JSON.stringify([provider, session.sessionId])
  for (const source of [...groups, ...worker.processes]) {
    for (const session of source.sessions) {
      const id = key(source.providerName, session)
      const previous = selected.get(id)
      if (!previous || sessionRank(session, nowMs) < sessionRank(previous, nowMs))
        selected.set(id, session)
    }
  }
  const keep = (provider: string, session: DashboardExternalSession) => selected.get(key(provider, session)) === session
  const servicePids = new Set(groups.map(group => group.servicePid))
  return {
    ...worker,
    observedProcessCount: worker.observedProcessCount ?? worker.processes.length,
    processes: worker.processes.flatMap((process) => {
      const sessions = process.sessions.filter(session => keep(process.providerName, session))
      if ((process.sessions.length > 0 && sessions.length === 0) || (sessions.length === 0 && servicePids.has(process.pid)))
        return []
      return [{ ...process, sessions }]
    }),
    serviceGroups: groups.map(group => ({ ...group, sessions: group.sessions.filter(session => keep(group.providerName, session)) })),
  }
}

export interface DashboardExternalSessionRow {
  session: DashboardExternalSession
  providerName: string
  pid: number
  source: 'process' | 'service'
}

export function externalWorkerSessionRows(worker: DashboardExternalWorker, nowMs: number): DashboardExternalSessionRow[] {
  if (worker.status !== 'available')
    return []
  const projected = projectExternalWorker(worker, nowMs)
  return [
    ...projected.processes.flatMap(process => process.sessions.map(session => ({ session, providerName: process.providerName, pid: process.pid, source: 'process' as const }))),
    ...(projected.serviceGroups ?? []).flatMap(group => group.sessions.map(session => ({ session, providerName: group.providerName, pid: group.servicePid, source: 'service' as const }))),
  ]
}

/** Count sessions, never native processes, service groups, or desktop helpers. */
export function externalAgentSessions(workers: readonly DashboardExternalWorker[], nowMs = Date.now()): DashboardExternalSession[] {
  return workers.flatMap(worker => externalWorkerSessionRows(worker, nowMs)
    .map(({ session }) => ({ ...session, status: resolveExternalSessionStatus(session, nowMs) })))
}

/** Search, counts, and rows share the same deduplication and freshness rules. */
export function filterExternalAgentWorkers(
  workers: readonly DashboardExternalWorker[],
  query: string,
  status: DashboardStatusFilter,
  nowMs = Date.now(),
): DashboardExternalWorker[] {
  const search = query.trim().toLocaleLowerCase()
  const matches = (values: readonly string[]) => values.some(value => value.toLocaleLowerCase().includes(search))
  return workers.flatMap((original) => {
    const workerMatches = !search || matches([original.workerName])
    if (original.status !== 'available')
      return workerMatches ? [original] : []
    const worker = projectExternalWorker(original, nowMs)
    const filterSessions = (sessions: readonly DashboardExternalSession[], sourceMatches: boolean) => sessions.filter(session =>
      (status === 'all' || resolveExternalSessionStatus(session, nowMs) === status)
      && (sourceMatches || matches([session.sessionId, session.displayName ?? ''])),
    )
    const processes = worker.processes.flatMap((process) => {
      const processMatches = workerMatches || matches([process.providerName, process.processName, process.kind, String(process.pid)])
      if (process.sessions.length === 0)
        return status === 'all' && processMatches ? [process] : []
      const sessions = filterSessions(process.sessions, processMatches)
      return sessions.length > 0 ? [{ ...process, sessions }] : []
    })
    const serviceGroups = (worker.serviceGroups ?? []).flatMap((group) => {
      const groupMatches = workerMatches || matches([group.providerName, group.processName, String(group.servicePid)])
      const sessions = filterSessions(group.sessions, groupMatches)
      return sessions.length > 0 || (status === 'all' && groupMatches) ? [{ ...group, sessions }] : []
    })
    return processes.length > 0 || serviceGroups.length > 0 || (workerMatches && status === 'all') ? [{ ...worker, processes, serviceGroups }] : []
  })
}
