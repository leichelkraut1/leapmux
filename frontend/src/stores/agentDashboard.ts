import type { AgentActivityStore } from './agentActivity.store'
import type { AgentTab, Tab } from './tab.types'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentActivityState, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { cmpStr } from '~/lib/crdt'
import { isAgentTab } from './tab.types'

export type DashboardAgentStatus = 'working' | 'waiting' | 'idle' | 'offline' | 'unknown'

export interface DashboardAgent {
  agentId: string
  workerId: string
  workerName: string
  workspaceId: string
  workspaceName: string
  title: string
  providerName: string
  status: DashboardAgentStatus
  statusDetail?: string
}

export interface AgentDashboardInput {
  /** The account's complete live tab projection, including archived workspaces. */
  tabs: readonly Tab[]
  workspaces: readonly { id: string, title: string }[]
  workers: readonly { id: string, online: boolean }[]
  workerName: (workerId: string) => string | undefined
  /** True only while this client's encrypted Worker channel is ready. */
  workerFresh: (workerId: string) => boolean
  providerName: (provider: AgentProvider | undefined) => string | undefined
  activity: Pick<AgentActivityStore, 'hasPublished' | 'publishedState'>
}

const STATUS_ORDER: Record<DashboardAgentStatus, number> = {
  waiting: 0,
  working: 1,
  idle: 2,
  unknown: 3,
  offline: 4,
}

function agentState(
  tab: AgentTab,
  online: boolean | undefined,
  input: AgentDashboardInput,
): Pick<DashboardAgent, 'status' | 'statusDetail'> {
  if (online === false)
    return { status: 'offline' }
  if (!tab.workerId || online === undefined || !input.workerFresh(tab.workerId))
    return { status: 'unknown' }
  if (tab.agentStatus === AgentStatus.STARTING)
    return { status: 'unknown', statusDetail: tab.startupMessage?.trim() || 'Starting agent' }
  if (tab.agentStatus === AgentStatus.STARTUP_FAILED)
    return { status: 'unknown', statusDetail: tab.startupError?.trim() || 'Agent failed to start' }
  if (tab.agentStatus === undefined || tab.agentStatus === AgentStatus.UNSPECIFIED
    || !input.activity.hasPublished(tab.id)) {
    return { status: 'unknown' }
  }

  // The Worker already combines turn, approval, and background-task activity.
  // A queue entry, unread dot, or ACTIVE process is not proof of ongoing work.
  switch (input.activity.publishedState(tab.id)) {
    case AgentActivityState.WAITING_FOR_USER:
      return { status: 'waiting' }
    case AgentActivityState.WORKING:
      return { status: 'working' }
    case AgentActivityState.IDLE:
      return { status: 'idle' }
    default:
      return { status: 'unknown' }
  }
}

/** Build a read-only overview; opening a row uses its existing workspace and tab. */
export function buildAgentDashboard(input: AgentDashboardInput): DashboardAgent[] {
  const workspaceNames = new Map(input.workspaces.map(workspace => [workspace.id, workspace.title]))
  const onlineByWorker = new Map(input.workers.map(worker => [worker.id, worker.online]))
  const rows = input.tabs.filter(isAgentTab).map((tab): DashboardAgent => {
    const workerId = tab.workerId ?? ''
    return {
      agentId: tab.id,
      workerId,
      workerName: input.workerName(workerId)?.trim() || workerId || 'Unknown PC',
      workspaceId: tab.workspaceId,
      workspaceName: workspaceNames.get(tab.workspaceId)?.trim() || 'Untitled workspace',
      title: tab.title?.trim() || 'Untitled agent',
      providerName: input.providerName(tab.agentProvider)?.trim() || 'Unknown provider',
      ...agentState(tab, onlineByWorker.get(workerId), input),
    }
  })
  return rows.sort((a, b) => STATUS_ORDER[a.status] - STATUS_ORDER[b.status]
    || cmpStr(a.workerName, b.workerName)
    || cmpStr(a.workspaceName, b.workspaceName)
    || cmpStr(a.title, b.title)
    || cmpStr(a.agentId, b.agentId))
}
