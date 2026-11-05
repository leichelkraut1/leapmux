import type { Component } from 'solid-js'
import type { DashboardAgentStatus } from '~/stores/agentDashboard'
import type { Tab } from '~/stores/tab.types'
import { Show, useContext } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { createStableContext } from '~/lib/createStableContext'
import { isAgentTab } from '~/stores/tab.types'
import * as styles from './AgentTreeStatus.css'

/** Uses the same Worker/channel/lifecycle freshness gates as the managed dashboard. */
export const AgentTreeStatusContext = createStableContext<(agentId: string) => DashboardAgentStatus>('workspace/AgentTreeStatus')

const LABELS: Record<DashboardAgentStatus, string> = {
  working: 'Running',
  waiting: 'Waiting',
  idle: 'Idle',
  offline: 'Offline',
  unknown: 'Unknown',
}
const DETAILS: Record<DashboardAgentStatus, string> = {
  working: 'The Worker reports this agent is running.',
  waiting: 'The Worker reports this agent is waiting for you.',
  idle: 'The Worker reports this agent is idle.',
  offline: 'The computer is offline; current agent activity is unknown.',
  unknown: 'Current agent activity has not been verified.',
}

export const AgentTreeStatus: Component<{ tab: Tab }> = (props) => {
  const statusFor = useContext(AgentTreeStatusContext)
  const status = () => statusFor?.(props.tab.id) ?? 'unknown'
  return (
    <Show when={statusFor && isAgentTab(props.tab)}>
      <Tooltip text={DETAILS[status()]}>
        <span class={`${styles.badge} ${styles.color[status()]}`} data-testid="agent-tree-status" data-status={status()}>
          {LABELS[status()]}
        </span>
      </Tooltip>
    </Show>
  )
}
