export type WorkerRegistryStatus = 'loading' | 'available' | 'unavailable'
export type DashboardStatusFilter = 'all' | 'working' | 'waiting' | 'idle'

export interface DashboardExternalSession {
  sessionId: string
  /** Provider-supplied name, normalized and bounded by the Worker. */
  displayName?: string
  status: 'working' | 'waiting' | 'idle' | 'unknown'
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

/** Count sessions, never native processes or desktop helpers. */
export function externalAgentSessions(workers: readonly DashboardExternalWorker[]): DashboardExternalSession[] {
  return workers.flatMap(worker => worker.status === 'available'
    ? worker.processes.flatMap(process => process.sessions)
    : [])
}

/** Apply the same search and status selection to each observed session. */
export function filterExternalAgentWorkers(
  workers: readonly DashboardExternalWorker[],
  query: string,
  status: DashboardStatusFilter,
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
        (status === 'all' || session.status === status)
        && (processMatches || [session.sessionId, session.displayName ?? '']
          .some(value => value.toLocaleLowerCase().includes(search))),
      )
      return sessions.length > 0 ? [{ ...process, sessions }] : []
    })
    return processes.length > 0 || (workerMatches && status === 'all') ? [{ ...worker, processes }] : []
  })
}
