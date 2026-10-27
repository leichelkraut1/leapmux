import type { Component } from 'solid-js'
import type { DashboardExternalProcess, DashboardExternalWorker, WorkerRegistryStatus } from '~/stores/externalAgentInventory'
import { createMemo, For, Show } from 'solid-js'
import * as styles from './AgentDashboard.css'

const KIND_LABELS: Record<DashboardExternalProcess['kind'], string> = {
  cli: 'CLI',
  desktop: 'Desktop',
  unknown: 'Process type unknown',
}

function validObservationTime(value: string | undefined): string | undefined {
  return value && Number.isFinite(Date.parse(value)) ? value : undefined
}

export const ExternalAgentProcesses: Component<{
  workers: readonly DashboardExternalWorker[]
  registryStatus?: WorkerRegistryStatus
  query: string
  onRefresh: (() => void) | undefined
}> = (props) => {
  const registryStatus = () => props.registryStatus ?? 'available'
  const loading = () => registryStatus() === 'loading'
    || (registryStatus() === 'available' && props.workers.some(worker => worker.status === 'loading'))
  const visibleWorkers = createMemo(() => {
    const staticSearch = props.query.trim().toLocaleLowerCase()
    return props.workers.flatMap((worker) => {
      if (!staticSearch || worker.workerName.toLocaleLowerCase().includes(staticSearch))
        return [worker]
      const processes = worker.status === 'available'
        ? worker.processes.filter(process => [process.providerName, process.processName, KIND_LABELS[process.kind], String(process.pid)]
            .some(value => value.toLocaleLowerCase().includes(staticSearch)))
        : []
      return processes.length > 0 ? [{ ...worker, processes }] : []
    })
  })

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
      <p class={styles.note}>Detected processes show which agent apps are open. Their chat activity is unknown. The status filters above apply to LeapMux chats.</p>
      <p class={styles.note}>Checks recognized native Claude and Codex executables for the Worker's account. Script-based installations may not appear.</p>
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
          <Show when={visibleWorkers().length > 0} fallback={<p class={styles.empty}>No matching external processes or computers.</p>}>
            <ul class={styles.agentList} aria-label="External agent computers">
              <For each={visibleWorkers()}>
                {worker => (
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
                    <Show when={worker.status === 'loading'}>
                      <p class={styles.observationMessage} role="status">Checking processes…</p>
                    </Show>
                    <Show when={worker.status === 'offline'}>
                      <p class={styles.observationMessage}>Computer offline</p>
                    </Show>
                    <Show when={worker.status === 'unavailable'}>
                      <p class={styles.observationMessage}>Observation unavailable</p>
                      <Show when={worker.statusDetail}>{detail => <p class={styles.note}>{detail()}</p>}</Show>
                    </Show>
                    <Show when={worker.status === 'available'}>
                      <Show
                        when={worker.processes.length > 0}
                        fallback={<p class={styles.observationMessage}>No detected agent processes</p>}
                      >
                        <ul class={styles.externalProcessList} aria-label={`Detected processes on ${worker.workerName}`}>
                          <For each={worker.processes}>
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
                      </Show>
                      <Show when={worker.totalCount !== undefined && worker.totalCount > worker.processes.length && !props.query.trim()}>
                        <p class={styles.note}>{`Showing ${worker.processes.length} of ${worker.totalCount} detected processes.`}</p>
                      </Show>
                    </Show>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </Show>
      </Show>
    </section>
  )
}
