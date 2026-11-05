import type { DashboardExternalServiceGroup, DashboardExternalSession, DashboardExternalWorker } from './externalAgentInventory'
import { describe, expect, it } from 'vitest'
import { externalAgentSessions, externalSessionObservationFreshness, externalSessionReportFreshness, filterExternalAgentWorkers, projectExternalWorker, resolveExternalSessionStatus } from './externalAgentInventory'

const reportedAt = '2026-10-27T12:00:00Z'
const reportExpiresAt = '2026-10-27T12:15:00Z'
const expiry = Date.parse(reportExpiresAt)
const report: DashboardExternalSession = {
  sessionId: 'reported-session',
  displayName: 'Plan rollout',
  status: 'working',
  agentReported: true,
  reportedAt,
  reportExpiresAt,
}

function worker(session: DashboardExternalSession): DashboardExternalWorker {
  return {
    workerId: 'pc',
    workerName: 'Windows PC',
    status: 'available',
    refreshing: true,
    processes: [{ pid: 123, providerName: 'Codex', processName: 'Codex', kind: 'desktop', sessions: [session] }],
  }
}

describe('resolveExternalSessionStatus', () => {
  it.each(['working', 'waiting', 'idle'] as const)('expires a reported %s state at the exact supplied deadline', (status) => {
    const session = { ...report, status }
    expect(resolveExternalSessionStatus(session, expiry - 1)).toBe(status)
    expect(resolveExternalSessionStatus(session, expiry)).toBe('unknown')
    expect(externalSessionReportFreshness(session, expiry)).toBe('expired')
    expect(session.status).toBe(status)
  })

  it.each([
    { reportedAt: '' },
    { reportedAt: 'invalid' },
    { reportExpiresAt: '' },
    { reportExpiresAt: 'invalid' },
    { reportExpiresAt: reportedAt },
    { reportExpiresAt: '2026-10-27T11:59:59Z' },
    { reportedAt: '2026-10-27T12:14:59.999Z' },
  ])('keeps invalid or future report metadata unknown: %j', (metadata) => {
    const session = { ...report, ...metadata }
    expect(resolveExternalSessionStatus(session, expiry - 2)).toBe('unknown')
    expect(externalSessionReportFreshness(session, expiry - 2)).toBe('unknown')
  })

  it('does not apply agent-report expiry to provider observations', () => {
    expect(resolveExternalSessionStatus({ sessionId: 'claude-session', status: 'idle' }, expiry)).toBe('idle')
    expect(resolveExternalSessionStatus({ ...report, agentReported: false }, expiry)).toBe('working')
  })
})

describe('externalAgentSessions', () => {
  it('uses the same expiry for totals and filtering while preserving an expired named report in all', () => {
    const workers = [worker(report)]
    expect(externalAgentSessions(workers, expiry - 1)[0]?.status).toBe('working')
    expect(filterExternalAgentWorkers(workers, 'PLAN ROLLOUT', 'working', expiry - 1)).toHaveLength(1)
    expect(externalAgentSessions(workers, expiry)[0]?.status).toBe('unknown')
    expect(filterExternalAgentWorkers(workers, 'plan rollout', 'working', expiry)).toEqual([])
    expect(filterExternalAgentWorkers(workers, 'plan rollout', 'all', expiry)[0]?.processes[0]?.sessions).toEqual([report])
    expect(workers[0]?.processes[0]?.sessions[0]?.status).toBe('working')
  })

  it.each(['loading', 'unavailable', 'offline'] as const)('excludes reports on a %s computer', (status) => {
    expect(externalAgentSessions([{ ...worker(report), status }], expiry - 1)).toEqual([])
  })
})

const observedAt = '2026-10-27T12:01:00Z'
const verifiedAt = '2026-10-27T12:01:20Z'
const observedExpiry = '2026-10-27T12:02:05Z'
const runtime: DashboardExternalSession = {
  sessionId: report.sessionId,
  displayName: 'Native rollout',
  status: 'waiting',
  statusObservedAt: observedAt,
  sourceVerifiedAt: verifiedAt,
  expiresAt: observedExpiry,
}
function service(sessions: readonly DashboardExternalSession[] = [runtime]): DashboardExternalServiceGroup {
  return {
    sourceId: 'native-source',
    providerName: 'Codex',
    servicePid: 800,
    processName: 'codex',
    available: true,
    partial: true,
    candidateCount: 25,
    candidateTruncated: true,
    subscriptionLimit: 8,
    sessions,
  }
}
function observedWorker(): DashboardExternalWorker {
  return { ...worker(report), serviceGroups: [service()] }
}

describe('projectExternalWorker', () => {
  it('prefers fresh runtime evidence over a duplicate report in rows, counts, and name search', () => {
    const now = Date.parse(verifiedAt)
    const original = observedWorker()
    expect(externalAgentSessions([original], now)).toEqual([expect.objectContaining({ displayName: 'Native rollout', status: 'waiting', runtimeObserved: true })])
    expect(projectExternalWorker(original, now).processes).toEqual([])
    expect(filterExternalAgentWorkers([original], 'native rollout', 'waiting', now)).toHaveLength(1)
    expect(filterExternalAgentWorkers([original], 'plan rollout', 'all', now)).toEqual([])
    expect(original.processes[0]?.sessions).toEqual([report])
    expect(original.serviceGroups?.[0]?.sessions).toEqual([runtime])
  })

  it('falls back to a fresh labeled report at native expiry without double counting', () => {
    const now = Date.parse(observedExpiry)
    const projected = projectExternalWorker(observedWorker(), now)
    expect(projected.serviceGroups?.[0]?.sessions).toEqual([])
    expect(externalAgentSessions([observedWorker()], now)).toEqual([expect.objectContaining({ agentReported: true, displayName: 'Plan rollout', status: 'working' })])
    expect(filterExternalAgentWorkers([observedWorker()], '', 'waiting', now)).toEqual([])
  })

  it('keeps expired runtime evidence unknown when there is no fresh report', () => {
    const item = { ...observedWorker(), processes: [] }
    const deadline = Date.parse(observedExpiry)
    expect(externalAgentSessions([item], deadline - 1)[0]?.status).toBe('waiting')
    expect(externalAgentSessions([item], deadline)[0]?.status).toBe('unknown')
    expect(filterExternalAgentWorkers([item], '', 'waiting', deadline)).toEqual([])
    expect(filterExternalAgentWorkers([item], '', 'all', deadline)[0]?.serviceGroups?.[0]?.sessions).toHaveLength(1)
  })

  it('does not equate session IDs from different providers or computers', () => {
    const item = observedWorker()
    item.processes = [{ ...item.processes[0]!, providerName: 'Claude', sessions: [{ sessionId: runtime.sessionId, status: 'idle' }] }]
    expect(externalAgentSessions([item, { ...observedWorker(), workerId: 'other' }], Date.parse(verifiedAt))).toHaveLength(3)
  })

  it('never counts empty or unavailable application services as idle agents', () => {
    const empty = { ...observedWorker(), processes: [], serviceGroups: [service([])] }
    expect(externalAgentSessions([empty], Date.parse(verifiedAt))).toEqual([])
    const unavailable = { ...empty, serviceGroups: [{ ...service(), available: false }] }
    expect(externalAgentSessions([unavailable], Date.parse(verifiedAt))[0]?.status).toBe('unknown')
    expect(filterExternalAgentWorkers([unavailable], '', 'idle', Date.parse(verifiedAt))).toEqual([])
  })

  it.each(['loading', 'unavailable', 'offline'] as const)('drops runtime session counts when the computer is %s', (status) => {
    expect(externalAgentSessions([{ ...observedWorker(), status }], Date.parse(verifiedAt))).toEqual([])
  })
})

describe('externalSessionObservationFreshness', () => {
  it('distinguishes activity evidence from a later source heartbeat without extending the supplied expiry', () => {
    expect(externalSessionObservationFreshness(runtime, Date.parse(verifiedAt))).toBe('fresh')
    expect(externalSessionObservationFreshness(runtime, Date.parse(observedExpiry))).toBe('expired')
  })

  it.each([
    { statusObservedAt: '' },
    { sourceVerifiedAt: 'invalid' },
    { expiresAt: '' },
    { expiresAt: verifiedAt },
    { statusObservedAt: '2026-10-27T12:01:21Z' },
    { sourceAvailable: false },
  ])('keeps invalid or unavailable evidence unknown: %j', (metadata) => {
    const session = { ...runtime, runtimeObserved: true, ...metadata }
    expect(externalSessionObservationFreshness(session, Date.parse(verifiedAt))).toBe('unknown')
    expect(resolveExternalSessionStatus(session, Date.parse(verifiedAt))).toBe('unknown')
  })
})
