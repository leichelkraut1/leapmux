import type { DashboardExternalSession, DashboardExternalWorker } from './externalAgentInventory'
import { describe, expect, it } from 'vitest'
import { externalAgentSessions, externalSessionReportFreshness, filterExternalAgentWorkers, resolveExternalSessionStatus } from './externalAgentInventory'

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
