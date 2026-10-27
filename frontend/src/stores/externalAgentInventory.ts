export type WorkerRegistryStatus = 'loading' | 'available' | 'unavailable'

/** Read-only process observations; process presence never determines chat activity. */
export interface DashboardExternalProcess {
  pid: number
  providerName: string
  processName: string
  kind: 'cli' | 'desktop' | 'unknown'
}

export interface DashboardExternalWorker {
  workerId: string
  workerName: string
  status: 'loading' | 'available' | 'unavailable' | 'offline'
  processes: readonly DashboardExternalProcess[]
  /** ISO timestamp of the observation, not the last chat activity. */
  observedAt?: string
  totalCount?: number
  statusDetail?: string
}
