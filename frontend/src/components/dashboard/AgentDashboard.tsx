import type { Component } from 'solid-js'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import type { DashboardAgent } from '~/stores/agentDashboard'
import type { DashboardExternalWorker, DashboardStatusFilter, WorkerRegistryStatus } from '~/stores/externalAgentInventory'
import { createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { PillGroup } from '~/components/common/PillGroup'
import { externalAgentSessions, filterExternalAgentWorkers } from '~/stores/externalAgentInventory'
import * as styles from './AgentDashboard.css'
import { ExternalAgentProcesses } from './ExternalAgentProcesses'

export interface AgentDashboardProps {
  rows: readonly DashboardAgent[]
  externalWorkers?: readonly DashboardExternalWorker[]
  registryStatus?: WorkerRegistryStatus
  onRefreshExternal?: () => void
  onOpenAgent: (row: DashboardAgent) => void
  onOpenExternalSession?: (conversation: NativeConversationRef) => void
  onClose: () => void
}

const FILTERS = [
  { key: 'all', label: 'All' },
  { key: 'working', label: 'Working' },
  { key: 'waiting', label: 'Waiting on you' },
  { key: 'idle', label: 'Idle' },
] as const

const STATUS_LABELS: Record<DashboardAgent['status'], string> = {
  working: 'Working',
  waiting: 'Waiting on you',
  idle: 'Idle',
  offline: 'Offline',
  unknown: 'Status unavailable',
}

export const AgentDashboard: Component<AgentDashboardProps> = (props) => {
  const [query, setQuery] = createSignal('')
  const [filter, setFilter] = createSignal<DashboardStatusFilter>('all')
  const [now, setNow] = createSignal(Date.now())
  // Cached observations and reports expire even while a Worker refresh is pending.
  onMount(() => {
    const expiryClock = setInterval(() => setNow(Date.now()), 1000)
    onCleanup(() => clearInterval(expiryClock))
  })
  const externalWorkers = createMemo(() => (props.registryStatus ?? 'available') === 'available' ? (props.externalWorkers ?? []) : [])
  const externalSessions = createMemo(() => externalAgentSessions(externalWorkers(), now()))
  const visibleExternalSessions = createMemo(() => externalAgentSessions(filterExternalAgentWorkers(externalWorkers(), query(), filter(), now()), now()))
  const counts = createMemo(() => {
    const result = { working: 0, waiting: 0, idle: 0, unavailable: 0 }
    for (const row of [...props.rows, ...externalSessions()]) {
      if (row.status === 'offline' || row.status === 'unknown')
        result.unavailable++
      else
        result[row.status]++
    }
    return result
  })
  const visibleRows = createMemo(() => {
    const staticSearch = query().trim().toLocaleLowerCase()
    const staticStatus = filter()
    return props.rows.filter(row =>
      (staticStatus === 'all' || row.status === staticStatus)
      && (!staticSearch || [row.title, row.workspaceName, row.providerName, row.workerName]
        .some(value => value.toLocaleLowerCase().includes(staticSearch))),
    )
  })

  const rowsById = createMemo(() => new Map(visibleRows().map(row => [row.agentId, row])))

  return (
    <Dialog title="Agents dashboard" wide tall onClose={props.onClose} data-testid="agent-dashboard">
      <div class={styles.dashboard}>
        <p class={styles.description}>LeapMux chats and native sessions across your computers.</p>

        <section aria-label="Agent status summary">
          <dl class={styles.summary}>
            <div class={styles.summaryItem} data-testid="agent-count-working">
              <dt>Working</dt>
              <dd class={styles.count}>{counts().working}</dd>
            </div>
            <div class={styles.summaryItem} data-testid="agent-count-waiting">
              <dt>Waiting on you</dt>
              <dd class={styles.count}>{counts().waiting}</dd>
            </div>
            <div class={styles.summaryItem} data-testid="agent-count-idle">
              <dt>Idle</dt>
              <dd class={styles.count}>{counts().idle}</dd>
            </div>
            <div class={styles.summaryItem} data-testid="agent-count-unavailable">
              <dt>Unavailable</dt>
              <dd class={styles.count}>{counts().unavailable}</dd>
            </div>
          </dl>
        </section>

        <div class={styles.controls}>
          <label class={styles.search}>
            Search agents
            <input
              type="search"
              placeholder="Agent, workspace, provider, or computer"
              value={query()}
              onInput={event => setQuery(event.currentTarget.value)}
            />
          </label>
          <PillGroup<DashboardStatusFilter>
            label="Filter agents by status"
            options={FILTERS}
            selectedKey={filter()}
            onSelect={setFilter}
          />
        </div>

        <p class={styles.resultCount} aria-live="polite">
          {`Showing ${visibleRows().length + visibleExternalSessions().length} of ${props.rows.length + externalSessions().length} agents`}
        </p>

        <div class={styles.results}>
          <section class={styles.managedSection} aria-label="LeapMux chats">
            <h2 class={styles.sectionTitle}>In LeapMux</h2>
            <Show
              when={props.rows.length > 0}
              fallback={(
                <div class={styles.empty}>
                  <strong>No agents yet</strong>
                  <p>Open an agent in a LeapMux workspace to see it here.</p>
                </div>
              )}
            >
              <Show
                when={visibleRows().length > 0}
                fallback={(
                  <div class={styles.empty}>
                    <strong>No matching agents</strong>
                    <p>Try another search or select All.</p>
                  </div>
                )}
              >
                <ul class={styles.agentList} aria-label="Agents">
                  <For each={[...rowsById().keys()]}>
                    {agentId => (
                      <Show when={rowsById().get(agentId)}>
                        {row => (
                          <li class={styles.agentRow} data-agent-id={row().agentId} data-worker-id={row().workerId}>
                            <div class={styles.identity}>
                              <h3 class={styles.agentTitle}>{row().title}</h3>
                              <span class={styles.secondary}>{row().providerName}</span>
                            </div>
                            <dl class={styles.location}>
                              <div class={styles.locationLine}>
                                <dt class={styles.secondary}>Workspace</dt>
                                <dd class={styles.locationValue}>{row().workspaceName}</dd>
                              </div>
                              <div class={styles.locationLine}>
                                <dt class={styles.secondary}>Computer</dt>
                                <dd class={styles.locationValue}>{row().workerName}</dd>
                              </div>
                            </dl>
                            <div class={styles.statusBlock}>
                              <span class={`${styles.status} ${styles.statusColor[row().status]}`} data-status={row().status}>
                                {STATUS_LABELS[row().status]}
                              </span>
                              <Show when={row().statusDetail}>
                                {detail => <span class={styles.statusDetail}>{detail()}</span>}
                              </Show>
                            </div>
                            <button
                              type="button"
                              class={styles.openButton}
                              aria-label={`Open chat: ${row().title} on ${row().workerName}`}
                              onClick={() => props.onOpenAgent(row())}
                            >
                              Open chat
                            </button>
                          </li>
                        )}
                      </Show>
                    )}
                  </For>
                </ul>
              </Show>
            </Show>
            <p class={styles.note}>Unavailable includes offline computers and agents whose status has not arrived, including expired observations and reports.</p>
          </section>
          <Show when={props.externalWorkers}>
            {workers => <ExternalAgentProcesses workers={workers()} nowMs={now()} query={query()} statusFilter={filter()} registryStatus={props.registryStatus ?? 'available'} onRefresh={props.onRefreshExternal} onOpenSession={props.onOpenExternalSession} />}
          </Show>
        </div>
      </div>
    </Dialog>
  )
}
