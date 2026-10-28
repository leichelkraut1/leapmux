import type { Component } from 'solid-js'
import type { DashboardExternalProcess, DashboardExternalSession, DashboardExternalWorker, DashboardStatusFilter, WorkerRegistryStatus } from '~/stores/externalAgentInventory'
import { createMemo, For, Show } from 'solid-js'
import { externalSessionObservationFreshness, externalSessionReportFreshness, externalWorkerSessionRows, filterExternalAgentWorkers, resolveExternalSessionStatus } from '~/stores/externalAgentInventory'
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
  nowMs: number
  statusFilter?: DashboardStatusFilter
  onRefresh: (() => void) | undefined
}> = (props) => {
  const registryStatus = () => props.registryStatus ?? 'available'
  const loading = () => registryStatus() === 'loading'
    || (registryStatus() === 'available' && props.workers.some(worker => worker.status === 'loading' || worker.refreshing))
  const visibleWorkers = createMemo(
    () => filterExternalAgentWorkers(props.workers, props.query, props.statusFilter ?? 'all', props.nowMs),
    undefined,
    // Clock ticks must not recreate unchanged rows and collapse expanded helpers.
    { equals: (previous, next) => JSON.stringify(previous) === JSON.stringify(next) },
  )

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
      <p class={styles.note}>Native session activity is observed read-only from supported providers and application services. Participating-agent claims remain labeled Agent-reported. Snapshots are checked every 15 seconds while this dashboard is open.</p>
      <p class={styles.note}>Fresh runtime observations take precedence over duplicate agent reports. Expired observations and reports become unknown. An application service can represent several chats; its PID is not an agent PID. Coverage may be partial, and processes without session status do not establish activity.</p>
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
                  const sessions = () => externalWorkerSessionRows(worker, props.nowMs)
                  const unobserved = () => worker.processes.filter(process => process.sessions.length === 0)
                  return (
                    <li class={styles.externalWorker} data-worker-id={worker.workerId} data-observation-status={worker.status}>
                      <div class={styles.sectionHeader}>
                        <h3 class={styles.agentTitle}>{worker.workerName}</h3>
                        <Show when={worker.status === 'available' && validObservationTime(worker.observedAt)}>
                          {time => (
                            <span class={styles.secondary}>
                              Checked at
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
                          <ul class={styles.externalProcessList} aria-label={`External sessions on ${worker.workerName}`}>
                            <For each={sessions()}>
                              {({ providerName, pid, source, session }) => {
                                const status = () => resolveExternalSessionStatus(session, props.nowMs)
                                const freshness = () => externalSessionReportFreshness(session, props.nowMs)
                                return (
                                  <li class={styles.externalProcess} data-session-id={session.sessionId}>
                                    <div class={styles.processIdentity}>
                                      <strong class={styles.agentTitle}>{session.displayName?.trim() || providerName}</strong>
                                      <span class={styles.secondary}>
                                        {`${session.displayName?.trim() ? `${providerName} · ` : ''}Session ${shortSessionId(session.sessionId)} · ${source === 'service' ? 'Service PID' : 'PID'} ${pid}`}
                                      </span>
                                    </div>
                                    <div class={styles.processObservation}>
                                      <span class={`${styles.status} ${styles.statusColor[status()]}`} data-status={status()}>
                                        {STATUS_LABELS[status()]}
                                      </span>
                                      <Show
                                        when={session.agentReported}
                                        fallback={(
                                          <Show when={session.runtimeObserved} fallback={<span class={styles.statusDetail}>Read-only observation</span>}>
                                            <span class={styles.statusDetail}>Read-only runtime observation · Application service</span>
                                            <span class={styles.statusDetail}>
                                              State observed
                                              {' '}
                                              <Show when={validObservationTime(session.statusObservedAt)} fallback="time unavailable">
                                                {time => <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>}
                                              </Show>
                                            </span>
                                            <span class={styles.statusDetail}>
                                              Source verified
                                              {' '}
                                              <Show when={validObservationTime(session.sourceVerifiedAt)} fallback="time unavailable">
                                                {time => <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>}
                                              </Show>
                                            </span>
                                            <span class={styles.statusDetail}>
                                              <Show
                                                when={externalSessionObservationFreshness(session, props.nowMs) === 'fresh'}
                                                fallback={externalSessionObservationFreshness(session, props.nowMs) === 'expired' ? 'Observation expired · Activity unknown' : 'Observation freshness unavailable · Activity unknown'}
                                              >
                                                Fresh source · Expires
                                                {' '}
                                                <time dateTime={session.expiresAt}>{new Date(session.expiresAt!).toLocaleString()}</time>
                                              </Show>
                                            </span>
                                          </Show>
                                        )}
                                      >
                                        <span class={styles.statusDetail}>
                                          Agent-reported · Last reported
                                          {' '}
                                          <Show when={validObservationTime(session.reportedAt)} fallback="time unavailable">
                                            {time => <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>}
                                          </Show>
                                        </span>
                                        <span class={styles.statusDetail}>
                                          <Show when={freshness() === 'fresh'} fallback={freshness() === 'expired' ? 'Report expired · Activity unknown' : 'Report freshness unavailable · Activity unknown'}>
                                            Fresh report · Expires
                                            {' '}
                                            <time dateTime={session.reportExpiresAt}>{new Date(session.reportExpiresAt!).toLocaleString()}</time>
                                          </Show>
                                        </span>
                                      </Show>
                                    </div>
                                  </li>
                                )
                              }}
                            </For>
                          </ul>
                        </Show>
                        <For each={worker.serviceGroups ?? []}>
                          {group => (
                            <div class={styles.serviceGroup} data-source-id={group.sourceId}>
                              <strong class={styles.agentTitle}>{`${group.providerName} application service`}</strong>
                              <span class={styles.secondary}>{`${group.processName} · Service PID ${group.servicePid}`}</span>
                              <p class={styles.observationMessage}>
                                {group.available ? 'Read-only source available' : 'Source unavailable · Session activity unknown'}
                              </p>
                              <Show when={group.available && group.sessions.length === 0}>
                                <p class={styles.note}>No matching observed sessions from this source.</p>
                              </Show>
                              <Show when={group.partial || group.candidateTruncated}>
                                <p class={styles.note}>{`Partial coverage · ${group.candidateCount} candidate sessions · Subscription limit ${group.subscriptionLimit}`}</p>
                              </Show>
                              <Show when={group.candidateTruncated}>
                                <p class={styles.note}>Candidate list truncated. Other sessions may not appear.</p>
                              </Show>
                            </div>
                          )}
                        </For>
                        <Show when={unobserved().length > 0}>
                          <details class={styles.unobservedProcesses}>
                            <summary class={styles.processSummary}>{`Processes without session status (${unobserved().length})`}</summary>
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
                        <Show when={worker.processes.length === 0 && (worker.serviceGroups?.length ?? 0) === 0}>
                          <p class={styles.observationMessage}>No detected agent processes</p>
                        </Show>
                        <Show when={worker.totalCount !== undefined && worker.totalCount > (worker.observedProcessCount ?? worker.processes.length) && !props.query.trim() && (props.statusFilter ?? 'all') === 'all'}>
                          <p class={styles.note}>{`Showing ${worker.observedProcessCount ?? worker.processes.length} of ${worker.totalCount} detected processes. Session totals may be incomplete.`}</p>
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
