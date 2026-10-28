import type { Component } from 'solid-js'
import type { DashboardExternalProcess, DashboardExternalSession, DashboardExternalWorker, DashboardStatusFilter, WorkerRegistryStatus } from '~/stores/externalAgentInventory'
import { createMemo, For, Show } from 'solid-js'
import { filterExternalAgentWorkers } from '~/stores/externalAgentInventory'
import * as styles from './AgentDashboard.css'

const KIND_LABELS: Record<DashboardExternalProcess['kind'], string> = {
  cli: 'CLI',
  desktop: 'Desktop',
  unknown: 'Process type unknown',
}

const STATUS_LABELS: Record<DashboardExternalSession['status'], string> = {
  working: 'Working',
  waiting: 'Waiting on you',
  idle: 'Idle',
  unknown: 'Status unavailable',
}

function validObservationTime(value: string | undefined): string | undefined {
  return value && Number.isFinite(Date.parse(value)) ? value : undefined
}

function shortSessionId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 12)}…` : id
}

export const ExternalAgentProcesses: Component<{
  workers: readonly DashboardExternalWorker[]
  registryStatus?: WorkerRegistryStatus
  query: string
  statusFilter?: DashboardStatusFilter
  onRefresh: (() => void) | undefined
}> = (props) => {
  const registryStatus = () => props.registryStatus ?? 'available'
  const loading = () => registryStatus() === 'loading'
    || (registryStatus() === 'available' && props.workers.some(worker => worker.status === 'loading' || worker.refreshing))
  const visibleWorkers = createMemo(() => filterExternalAgentWorkers(props.workers, props.query, props.statusFilter ?? 'all'))

  return (
    <section class={styles.externalSection} aria-label="Outside LeapMux">
      <div class={styles.sectionHeader}>
        <h2 class={styles.sectionTitle}>Outside LeapMux</h2>
        <Show when={props.onRefresh}>
          <button
            type="button"
            disabled={loading()}
            aria-label="Refresh external processes"
            onClick={() => props.onRefresh?.()}
          >
            Refresh
          </button>
        </Show>
      </div>
      <p class={styles.note}>Read-only Claude Code session snapshots, including supported sessions in Claude Desktop and VS Code. Status is checked every 15 seconds while this dashboard is open.</p>
      <p class={styles.note}>Counts and filters include observed sessions. Current Codex Desktop versions do not expose session activity here. Unobserved processes are listed separately with unknown activity; some installations may not appear.</p>
      <Show
        when={registryStatus() === 'available'}
        fallback={(
          <p class={styles.observationMessage} role="status">
            {registryStatus() === 'loading' ? 'Loading computers…' : 'Computer list unavailable. Refresh to try again.'}
          </p>
        )}
      >
        <Show
          when={props.workers.length > 0}
          fallback={<p class={styles.empty}>No computers available. Connect a LeapMux Worker on each computer to check for agent processes.</p>}
        >
          <Show when={visibleWorkers().length > 0} fallback={<p class={styles.empty}>No matching external sessions, processes, or computers.</p>}>
            <ul class={styles.agentList} aria-label="External agent computers">
              <For each={visibleWorkers()}>
                {(worker) => {
                  const sessions = () => worker.processes.flatMap(process => process.sessions.map(session => ({ process, session })))
                  const unobserved = () => worker.processes.filter(process => process.sessions.length === 0)
                  return (
                    <li class={styles.externalWorker} data-worker-id={worker.workerId} data-observation-status={worker.status}>
                      <div class={styles.sectionHeader}>
                        <h3 class={styles.agentTitle}>{worker.workerName}</h3>
                        <Show when={worker.status === 'available' && validObservationTime(worker.observedAt)}>
                          {time => (
                            <span class={styles.secondary}>
                              Observed at
                              {' '}
                              <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>
                            </span>
                          )}
                        </Show>
                      </div>
                      <Show when={worker.status === 'available' && worker.refreshing}>
                        <p class={styles.observationMessage} role="status">Updating observation…</p>
                      </Show>
                      <Show when={worker.status === 'loading'}>
                        <p class={styles.observationMessage} role="status">Checking sessions and processes…</p>
                      </Show>
                      <Show when={worker.status === 'offline'}>
                        <p class={styles.observationMessage}>Computer offline · Session activity unknown</p>
                      </Show>
                      <Show when={worker.status === 'unavailable'}>
                        <p class={styles.observationMessage}>Observation unavailable · Session activity unknown</p>
                        <Show when={worker.statusDetail}>{detail => <p class={styles.note}>{detail()}</p>}</Show>
                      </Show>
                      <Show when={worker.status === 'available'}>
                        <Show when={sessions().length > 0}>
                          <ul class={styles.externalProcessList} aria-label={`Observed sessions on ${worker.workerName}`}>
                            <For each={sessions()}>
                              {({ process, session }) => (
                                <li class={styles.externalProcess} data-session-id={session.sessionId}>
                                  <div class={styles.processIdentity}>
                                    <strong class={styles.agentTitle}>{session.displayName?.trim() || process.providerName}</strong>
                                    <span class={styles.secondary}>
                                      {`${session.displayName?.trim() ? `${process.providerName} · ` : ''}Session ${shortSessionId(session.sessionId)} · PID ${process.pid}`}
                                    </span>
                                  </div>
                                  <div class={styles.processObservation}>
                                    <span class={`${styles.status} ${styles.statusColor[session.status]}`} data-status={session.status}>
                                      {STATUS_LABELS[session.status]}
                                    </span>
                                    <span class={styles.statusDetail}>Read-only observation</span>
                                  </div>
                                </li>
                              )}
                            </For>
                          </ul>
                        </Show>
                        <Show when={unobserved().length > 0}>
                          <details class={styles.unobservedProcesses}>
                            <summary class={styles.processSummary}>{`Processes without session observations (${unobserved().length})`}</summary>
                            <p class={styles.note}>A desktop app can have several helper processes. These are not counted as agents.</p>
                            <ul class={styles.externalProcessList} aria-label={`Detected processes on ${worker.workerName}`}>
                              <For each={unobserved()}>
                                {process => (
                                  <li class={styles.externalProcess}>
                                    <div class={styles.processIdentity}>
                                      <strong class={styles.agentTitle}>{process.providerName}</strong>
                                      <span class={styles.secondary}>{`${KIND_LABELS[process.kind]} · ${process.processName} · PID ${process.pid}`}</span>
                                    </div>
                                    <div class={styles.processObservation}>
                                      <span class={styles.status}>Process detected</span>
                                      <span class={styles.statusDetail}>Chat activity unknown</span>
                                    </div>
                                  </li>
                                )}
                              </For>
                            </ul>
                          </details>
                        </Show>
                        <Show when={worker.processes.length === 0}>
                          <p class={styles.observationMessage}>No detected agent processes</p>
                        </Show>
                        <Show when={worker.totalCount !== undefined && worker.totalCount > worker.processes.length && !props.query.trim() && (props.statusFilter ?? 'all') === 'all'}>
                          <p class={styles.note}>{`Showing ${worker.processes.length} of ${worker.totalCount} detected processes. Session totals may be incomplete.`}</p>
                        </Show>
                      </Show>
                    </li>
                  )
                }}
              </For>
            </ul>
          </Show>
        </Show>
      </Show>
    </section>
  )
}
