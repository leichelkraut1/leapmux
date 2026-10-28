import type { Component } from 'solid-js'
import type { DashboardExternalProcess, DashboardExternalSession, DashboardExternalSessionRow, DashboardExternalWorker, DashboardStatusFilter, WorkerRegistryStatus } from '~/stores/externalAgentInventory'
import { createMemo, For, Show } from 'solid-js'
import { externalSessionObservationFreshness, externalSessionReportFreshness, externalWorkerSessionRows, filterExternalAgentWorkers, resolveExternalSessionStatus } from '~/stores/externalAgentInventory'
import { srOnly } from '~/styles/shared.css'
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

const ExternalSession: Component<{
  row: DashboardExternalSessionRow
  nowMs: number
}> = (props) => {
  const session = () => props.row.session
  const status = () => resolveExternalSessionStatus(session(), props.nowMs)
  const freshness = () => externalSessionReportFreshness(session(), props.nowMs)

  return (
    <li class={styles.externalProcess} data-session-id={session().sessionId}>
      <div class={styles.processIdentity}>
        <strong class={styles.agentTitle}>{session().displayName?.trim() || props.row.providerName}</strong>
        <span class={styles.secondary}>
          {`${session().displayName?.trim() ? `${props.row.providerName} · ` : ''}Session ${shortSessionId(session().sessionId)} · ${props.row.source === 'service' ? 'Service PID' : 'PID'} ${props.row.pid}`}
        </span>
      </div>
      <div class={styles.processObservation}>
        <span class={`${styles.status} ${styles.statusColor[status()]}`} data-status={status()}>
          {STATUS_LABELS[status()]}
        </span>
        <Show
          when={session().agentReported}
          fallback={(
            <Show when={session().runtimeObserved} fallback={<span class={styles.statusDetail}>Read-only observation</span>}>
              <span class={styles.statusDetail}>Read-only runtime observation · Application service</span>
              <span class={styles.statusDetail}>
                State observed
                {' '}
                <Show when={validObservationTime(session().statusObservedAt)} fallback="time unavailable">
                  {time => <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>}
                </Show>
              </span>
              <span class={styles.statusDetail}>
                Source verified
                {' '}
                <Show when={validObservationTime(session().sourceVerifiedAt)} fallback="time unavailable">
                  {time => <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>}
                </Show>
              </span>
              <span class={styles.statusDetail}>
                <Show
                  when={externalSessionObservationFreshness(session(), props.nowMs) === 'fresh'}
                  fallback={externalSessionObservationFreshness(session(), props.nowMs) === 'expired' ? 'Observation expired · Activity unknown' : 'Observation freshness unavailable · Activity unknown'}
                >
                  Fresh source · Expires
                  {' '}
                  <time dateTime={session().expiresAt}>{new Date(session().expiresAt!).toLocaleString()}</time>
                </Show>
              </span>
            </Show>
          )}
        >
          <span class={styles.statusDetail}>
            Agent-reported · Last reported
            {' '}
            <Show when={validObservationTime(session().reportedAt)} fallback="time unavailable">
              {time => <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>}
            </Show>
          </span>
          <span class={styles.statusDetail}>
            <Show when={freshness() === 'fresh'} fallback={freshness() === 'expired' ? 'Report expired · Activity unknown' : 'Report freshness unavailable · Activity unknown'}>
              Fresh report · Expires
              {' '}
              <time dateTime={session().reportExpiresAt}>{new Date(session().reportExpiresAt!).toLocaleString()}</time>
            </Show>
          </span>
        </Show>
      </div>
    </li>
  )
}

const ExternalWorker: Component<{
  worker: DashboardExternalWorker
  nowMs: number
  query: string
  statusFilter: DashboardStatusFilter
}> = (props) => {
  const sessionsById = createMemo(() => new Map(
    externalWorkerSessionRows(props.worker, props.nowMs)
      .map(row => [JSON.stringify([row.providerName, row.session.sessionId]), row]),
  ))
  const groupsById = createMemo(() => new Map((props.worker.serviceGroups ?? []).map(group => [group.sourceId, group])))
  const processesById = createMemo(() => new Map(props.worker.processes.filter(process => process.sessions.length === 0).map(process => [process.pid, process])))

  return (
    <li class={styles.externalWorker} data-worker-id={props.worker.workerId} data-observation-status={props.worker.status} aria-busy={props.worker.refreshing}>
      <div class={styles.sectionHeader}>
        <h3 class={styles.agentTitle}>{props.worker.workerName}</h3>
        <Show when={props.worker.status === 'available' && validObservationTime(props.worker.observedAt)}>
          {time => (
            <span class={styles.secondary}>
              Checked at
              {' '}
              <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>
            </span>
          )}
        </Show>
      </div>
      <Show when={props.worker.status === 'available' && props.worker.refreshing}>
        <p class={srOnly} role="status">Updating observation…</p>
      </Show>
      <Show when={props.worker.status === 'loading'}>
        <p class={styles.observationMessage} role="status">Checking sessions and processes…</p>
      </Show>
      <Show when={props.worker.status === 'offline'}>
        <p class={styles.observationMessage}>Computer offline · Session activity unknown</p>
      </Show>
      <Show when={props.worker.status === 'unavailable'}>
        <p class={styles.observationMessage}>Observation unavailable · Session activity unknown</p>
        <Show when={props.worker.statusDetail}>{detail => <p class={styles.note}>{detail()}</p>}</Show>
      </Show>
      <Show when={props.worker.status === 'available'}>
        <Show when={sessionsById().size > 0}>
          <ul class={styles.externalProcessList} aria-label={`External sessions on ${props.worker.workerName}`}>
            <For each={[...sessionsById().keys()]}>
              {sessionId => (
                <Show when={sessionsById().get(sessionId)}>
                  {row => <ExternalSession row={row()} nowMs={props.nowMs} />}
                </Show>
              )}
            </For>
          </ul>
        </Show>
        <For each={[...groupsById().keys()]}>
          {sourceId => (
            <Show when={groupsById().get(sourceId)}>
              {group => (
                <div class={styles.serviceGroup} data-source-id={group().sourceId}>
                  <strong class={styles.agentTitle}>{`${group().providerName} application service`}</strong>
                  <span class={styles.secondary}>{`${group().processName} · Service PID ${group().servicePid}`}</span>
                  <p class={styles.observationMessage}>
                    {group().available ? 'Read-only source available' : 'Source unavailable · Session activity unknown'}
                  </p>
                  <Show when={group().available && group().sessions.length === 0}>
                    <p class={styles.note}>No matching observed sessions from this source.</p>
                  </Show>
                  <Show when={group().partial || group().candidateTruncated}>
                    <p class={styles.note}>{`Partial coverage · ${group().candidateCount} candidate sessions · Subscription limit ${group().subscriptionLimit}`}</p>
                  </Show>
                  <Show when={group().candidateTruncated}>
                    <p class={styles.note}>Candidate list truncated. Other sessions may not appear.</p>
                  </Show>
                </div>
              )}
            </Show>
          )}
        </For>
        <Show when={processesById().size > 0}>
          <details class={styles.unobservedProcesses}>
            <summary class={styles.processSummary}>{`Processes without session status (${processesById().size})`}</summary>
            <p class={styles.note}>A desktop app can have several helper processes. These are not counted as agents.</p>
            <ul class={styles.externalProcessList} aria-label={`Detected processes on ${props.worker.workerName}`}>
              <For each={[...processesById().keys()]}>
                {pid => (
                  <Show when={processesById().get(pid)}>
                    {process => (
                      <li class={styles.externalProcess}>
                        <div class={styles.processIdentity}>
                          <strong class={styles.agentTitle}>{process().providerName}</strong>
                          <span class={styles.secondary}>{`${KIND_LABELS[process().kind]} · ${process().processName} · PID ${process().pid}`}</span>
                        </div>
                        <div class={styles.processObservation}>
                          <span class={styles.status}>Process detected</span>
                          <span class={styles.statusDetail}>Chat activity unknown</span>
                        </div>
                      </li>
                    )}
                  </Show>
                )}
              </For>
            </ul>
          </details>
        </Show>
        <Show when={props.worker.processes.length === 0 && (props.worker.serviceGroups?.length ?? 0) === 0}>
          <p class={styles.observationMessage}>No detected agent processes</p>
        </Show>
        <Show when={props.worker.totalCount !== undefined && props.worker.totalCount > (props.worker.observedProcessCount ?? props.worker.processes.length) && !props.query.trim() && (props.statusFilter ?? 'all') === 'all'}>
          <p class={styles.note}>{`Showing ${props.worker.observedProcessCount ?? props.worker.processes.length} of ${props.worker.totalCount} detected processes. Session totals may be incomplete.`}</p>
        </Show>
      </Show>
    </li>
  )
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
  // Projections contain fresh objects on every snapshot and expiry tick. Key
  // the view by identity so updated metadata does not remount expanded rows.
  const workersById = createMemo(() => new Map(
    filterExternalAgentWorkers(props.workers, props.query, props.statusFilter ?? 'all', props.nowMs)
      .map(worker => [worker.workerId, worker]),
  ))

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
          <Show when={workersById().size > 0} fallback={<p class={styles.empty}>No matching external sessions, processes, or computers.</p>}>
            <ul class={styles.agentList} aria-label="External agent computers">
              <For each={[...workersById().keys()]}>
                {workerId => (
                  <Show when={workersById().get(workerId)}>
                    {worker => (
                      <ExternalWorker
                        worker={worker()}
                        nowMs={props.nowMs}
                        query={props.query}
                        statusFilter={props.statusFilter ?? 'all'}
                      />
                    )}
                  </Show>
                )}
              </For>
            </ul>
          </Show>
        </Show>
      </Show>
    </section>
  )
}
