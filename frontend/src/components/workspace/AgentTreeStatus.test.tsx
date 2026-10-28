import type { AgentTab } from '~/stores/tab.types'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentActivityState, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createAgentActivityStore } from '~/stores/agentActivity.store'
import { buildAgentDashboard } from '~/stores/agentDashboard'
import { AgentTreeStatus, AgentTreeStatusContext } from './AgentTreeStatus'

afterEach(cleanup)

const tab: AgentTab = { id: 'agent', type: TabType.AGENT, workspaceId: 'work', workerId: 'pc', agentStatus: AgentStatus.ACTIVE }

describe('AgentTreeStatus', () => {
  it('follows published managed activity while retaining offline and unknown freshness gates', () => {
    const activity = createAgentActivityStore()
    const [online, setOnline] = createSignal(true)
    const [fresh, setFresh] = createSignal(true)
    const statusFor = () => buildAgentDashboard({
      tabs: [tab],
      workspaces: [{ id: 'work', title: 'Work' }],
      workers: [{ id: 'pc', online: online() }],
      workerName: () => 'PC',
      workerFresh: fresh,
      providerName: () => 'Provider',
      activity,
    })[0]!.status
    render(() => <AgentTreeStatusContext.Provider value={statusFor}><AgentTreeStatus tab={tab} /></AgentTreeStatusContext.Provider>)
    expect(screen.getByTestId('agent-tree-status')).toHaveTextContent('Unknown')
    activity.apply('agent', AgentActivityState.WORKING)
    expect(screen.getByTestId('agent-tree-status')).toHaveTextContent('Running')
    activity.apply('agent', AgentActivityState.WAITING_FOR_USER)
    expect(screen.getByTestId('agent-tree-status')).toHaveTextContent('Waiting')
    activity.apply('agent', AgentActivityState.IDLE)
    expect(screen.getByTestId('agent-tree-status')).toHaveTextContent('Idle')
    setOnline(false)
    expect(screen.getByTestId('agent-tree-status')).toHaveTextContent('Offline')
    setFresh(false)
    setOnline(true)
    expect(screen.getByTestId('agent-tree-status')).toHaveTextContent('Unknown')
    expect(screen.queryByText('Idle')).not.toBeInTheDocument()
  })

  it('does not add agent activity to terminal tabs or an unconnected tree surface', () => {
    render(() => (
      <>
        <AgentTreeStatus tab={tab} />
        <AgentTreeStatusContext.Provider value={() => 'working'}>
          <AgentTreeStatus tab={{ id: 'terminal', type: TabType.TERMINAL, workspaceId: 'work' }} />
        </AgentTreeStatusContext.Provider>
      </>
    ))
    expect(screen.queryByTestId('agent-tree-status')).not.toBeInTheDocument()
  })
})
