import type { RunRow, WorkflowComputer } from '~/stores/workflows'
import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { RefreshButton } from '~/components/common/RefreshButton'
import { WorkflowRunStatus } from '~/generated/proto/leapmux/v1/workflow_pb'
import { aggregateRuns, computerName, runStatusView, workflowErrorMessage } from '~/stores/workflows'
import { ComputerLabel, StatusBadge } from './ComputerPicker'
import * as styles from './Workflows.css'

export interface WorkflowRunsListProps {
  computers: readonly WorkflowComputer[]
  onOpenRun: (row: RunRow) => void
}

/**
 * Every run on every online computer. A run lives on its coordinator alone,
 * so this asks each computer for its own and merges the answers; a computer
 * that does not answer is named rather than silently left out.
 */
export function WorkflowRunsList(props: WorkflowRunsListProps) {
  const [rows, setRows] = createSignal<RunRow[]>([])
  const [loading, setLoading] = createSignal(false)
  const [loaded, setLoaded] = createSignal(false)
  const [failures, setFailures] = createSignal<string[]>([])
  let generation = 0
  onCleanup(() => {
    generation++
  })

  const load = async () => {
    const mine = ++generation
    const online = props.computers.filter(c => c.online)
    setLoading(true)
    const settled = await Promise.allSettled(online.map(async c => ({
      workerId: c.id,
      runs: (await workerRpc.listWorkflowRuns(c.id, {})).runs,
    })))
    if (mine !== generation)
      return
    const answered: { workerId: string, runs: Awaited<ReturnType<typeof workerRpc.listWorkflowRuns>>['runs'] }[] = []
    const failed: string[] = []
    settled.forEach((result, i) => {
      const computer = online[i]
      if (!computer)
        return
      if (result.status === 'fulfilled')
        answered.push(result.value)
      else
        failed.push(`${computer.name}: ${workflowErrorMessage(result.reason)}`)
    })
    setRows(aggregateRuns(answered))
    setFailures(failed)
    setLoading(false)
    setLoaded(true)
  }

  // Again whenever a computer comes online or goes away.
  createEffect(on(() => props.computers.filter(c => c.online).map(c => c.id).join('\n'), () => {
    void load()
  }))

  return (
    <div class={styles.form}>
      <div class={styles.sectionHeader}>
        <p class={styles.description}>Runs on every online computer. Open one to follow it beside its Lead's chat.</p>
        <RefreshButton onClick={() => void load()} disabled={loading()} title="Refresh runs" />
      </div>
      <Show when={failures().length > 0}>
        <ul class={styles.list} aria-label="Computers that did not answer">
          <For each={failures()}>
            {failure => <li class={styles.hint}>{failure}</li>}
          </For>
        </ul>
      </Show>
      <Show
        when={rows().length > 0}
        fallback={(
          <div class={styles.empty}>
            <strong>{loaded() ? 'No runs yet' : 'Loading runs...'}</strong>
            <Show when={loaded()}>
              <p>Launch a template from the Templates tab.</p>
            </Show>
          </div>
        )}
      >
        <ul class={styles.list} aria-label="Workflow runs">
          <For each={rows()}>
            {(row) => {
              const run = () => row.summary.run
              const status = () => runStatusView(run()?.status ?? WorkflowRunStatus.UNSPECIFIED)
              return (
                <li>
                  <button
                    type="button"
                    class={styles.rowButton}
                    data-testid="workflow-run-row"
                    aria-label={`Open run ${run()?.name ?? ''} on ${computerName(props.computers, row.coordinatorWorkerId)}`}
                    onClick={() => props.onOpenRun(row)}
                  >
                    <div class={styles.identity}>
                      <span class={styles.inline}>
                        <strong>{run()?.name}</strong>
                        <StatusBadge label={status().label} tone={status().tone} />
                      </span>
                      <Show when={run()?.statusDetail}>
                        <span class={styles.secondary}>{run()?.statusDetail}</span>
                      </Show>
                      <ComputerLabel computers={props.computers} workerId={row.coordinatorWorkerId} />
                    </div>
                    <div class={styles.summaryStats}>
                      <span>{`${row.summary.tasksSucceeded}/${row.summary.tasksTotal} tasks done`}</span>
                      <span>{`${row.summary.tasksActive} active`}</span>
                      <span>{`${row.summary.tasksBlocked} blocked`}</span>
                      <span>{`${row.summary.approvalsPending} pending approval${row.summary.approvalsPending === 1 ? '' : 's'}`}</span>
                    </div>
                  </button>
                </li>
              )
            }}
          </For>
        </ul>
      </Show>
    </div>
  )
}
