import type { AgentDashboardInput } from './agentDashboard'
import type { AgentTab } from './tab.types'
import { describe, expect, it } from 'vitest'
import { AgentActivityState, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createAgentActivityStore } from './agentActivity.store'
import { buildAgentDashboard } from './agentDashboard'

function agent(id: string, fields: Partial<AgentTab> = {}): AgentTab {
  return {
    id,
    type: TabType.AGENT,
    workspaceId: 'work',
    workerId: 'windows',
    agentStatus: AgentStatus.ACTIVE,
    title: id,
    ...fields,
  }
}

function input(tabs: AgentDashboardInput['tabs']): AgentDashboardInput {
  const names: Record<string, string> = { windows: 'Windows PC', mac: 'Mac' }
  return {
    tabs,
    workspaces: [{ id: 'work', title: 'Work' }, { id: 'archived', title: 'Older work' }],
    workers: [{ id: 'windows', online: true }, { id: 'mac', online: true }],
    workerName: id => names[id],
    workerFresh: () => true,
    providerName: () => 'Claude Code',
    activity: createAgentActivityStore(),
  }
}

describe('buildAgentDashboard', () => {
  it('shows agents across PCs and archived workspaces with waiting agents first', () => {
    const state = input([
      agent('idle'),
      agent('working', { workerId: 'mac', workspaceId: 'archived' }),
      agent('waiting'),
    ])
    const activity = createAgentActivityStore()
    activity.seedPublished('idle', AgentActivityState.IDLE)
    activity.seedPublished('working', AgentActivityState.WORKING)
    activity.seedPublished('waiting', AgentActivityState.WAITING_FOR_USER)
    state.activity = activity

    const rows = buildAgentDashboard(state)
    expect(rows.map(row => [row.agentId, row.status])).toEqual([
      ['waiting', 'waiting'],
      ['working', 'working'],
      ['idle', 'idle'],
    ])
    expect(rows[1]).toMatchObject({ workerName: 'Mac', workspaceName: 'Older work' })
    expect(state.tabs.map(tab => tab.id)).toEqual(['idle', 'working', 'waiting'])
  })

  it('does not report idle before an authoritative activity publication arrives', () => {
    const state = input([agent('unreported')])
    expect(buildAgentDashboard(state)[0]?.status).toBe('unknown')

    const activity = createAgentActivityStore()
    activity.seedPublished('unreported', AgentActivityState.IDLE)
    state.activity = activity
    expect(buildAgentDashboard(state)[0]?.status).toBe('idle')
  })

  it.each([AgentActivityState.IDLE, AgentActivityState.WORKING, AgentActivityState.WAITING_FOR_USER])(
    'does not reuse stale activity %s while a PC channel reconnects',
    (level) => {
      const state = input([agent('stale')])
      const activity = createAgentActivityStore()
      activity.seedPublished('stale', level)
      state.activity = activity
      state.workerFresh = () => false
      expect(buildAgentDashboard(state)[0]?.status).toBe('unknown')

      state.workers = [{ id: 'windows', online: false }]
      expect(buildAgentDashboard(state)[0]?.status).toBe('offline')
    },
  )

  it('keeps unknown Worker inventory distinct from a confirmed offline PC', () => {
    const state = input([agent('missing'), agent('unplaced', { workerId: undefined })])
    state.workers = []
    expect(buildAgentDashboard(state).map(row => row.status)).toEqual(['unknown', 'unknown'])
  })

  it('does not call incomplete or failed startup metadata idle', () => {
    const state = input([
      agent('missing', { agentStatus: undefined }),
      agent('starting', { agentStatus: AgentStatus.STARTING, startupMessage: 'Loading Claude' }),
      agent('failed', { agentStatus: AgentStatus.STARTUP_FAILED, startupError: 'Executable missing' }),
    ])
    const activity = createAgentActivityStore()
    for (const tab of state.tabs)
      activity.seedPublished(tab.id, AgentActivityState.IDLE)
    state.activity = activity

    const rows = buildAgentDashboard(state)
    expect(rows.every(row => row.status === 'unknown')).toBe(true)
    expect(rows.find(row => row.agentId === 'starting')?.statusDetail).toBe('Loading Claude')
    expect(rows.find(row => row.agentId === 'failed')?.statusDetail).toBe('Executable missing')
  })

  it('uses only projected agent tabs and keeps an explicitly opened subagent', () => {
    const state = input([
      { id: 'terminal', workspaceId: 'work', type: TabType.TERMINAL },
      { id: 'file', workspaceId: 'work', type: TabType.FILE },
      agent('parent'),
      agent('child', { parentAgentId: 'parent', rootAgentId: 'parent' }),
    ])
    expect(buildAgentDashboard(state).map(row => row.agentId)).toEqual(['child', 'parent'])

    state.tabs = [agent('parent')]
    expect(buildAgentDashboard(state).map(row => row.agentId)).toEqual(['parent'])
  })

  it('keeps readable fallbacks while names and provider metadata load', () => {
    const state = input([agent('unnamed', { title: ' ', workspaceId: 'unknown', workerId: undefined })])
    state.providerName = () => undefined
    expect(buildAgentDashboard(state)[0]).toMatchObject({
      title: 'Untitled agent',
      workspaceName: 'Untitled workspace',
      workerName: 'Unknown PC',
      providerName: 'Unknown provider',
      status: 'unknown',
    })
  })
})
