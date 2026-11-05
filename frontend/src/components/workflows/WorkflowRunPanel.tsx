import type {
  WorkflowApproval,
  WorkflowRoleAssignment,
  WorkflowRunAgent,
  WorkflowRunDetail,
  WorkflowTask,
} from '~/generated/proto/leapmux/v1/workflow_pb'
import type { RoleLaunchDraft, RunRef, WorkflowComputer } from '~/stores/workflows'
import X from 'lucide-solid/icons/x'
import { createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Alert } from '~/components/common/Alert'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { IconButton } from '~/components/common/IconButton'
import {
  WorkflowApprovalStatus,
  WorkflowEventKind,
  WorkflowRunAction,
  WorkflowRunStatus,
  WorkflowTaskAction,
} from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  agentStatusView,
  approvalKindLabel,
  approvalStatusView,
  buildRoleUpdate,
  canCancelRun,
  canCancelTask,
  canDeleteRun,
  canPauseRun,
  canReassignTask,
  canResumeRun,
  canRetryTask,
  computerName,
  eventKindLabel,
  isRunFinal,
  modelSummary,
  placementSummary,
  runStatusView,
  taskOutcomeView,
  taskStatusView,
  workflowErrorMessage,
} from '~/stores/workflows'
import { ComputerLabel, ComputerPicker, StatusBadge } from './ComputerPicker'
import { createWorkflowAction } from './createWorkflowAction'
import * as panelStyles from './WorkflowRunPanel.css'
import * as styles from './Workflows.css'

/** How often the open panel re-reads its run. */
export const RUN_POLL_INTERVAL_MS = 3000

export interface OpenAgentTarget {
  agentId: string
  workerId: string
  workspaceId: string
}

export interface WorkflowRunPanelProps {
  runRef: RunRef
  computers: readonly WorkflowComputer[]
  /** Focus the agent's tab, opening one in the run's workspace when none exists. */
  onOpenAgent: (target: OpenAgentTarget) => void
  /** Called once, with the run's Lead, when the run first loads. */
  onLeadKnown?: (target: OpenAgentTarget) => void
  /** The run was deleted. */
  onDeleted?: () => void
  onClose: () => void
}

function formatTime(ms: bigint): string {
  if (!ms)
    return ''
  return new Date(Number(ms)).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function WorkflowRunPanel(props: WorkflowRunPanelProps) {
  const [detail, setDetail] = createSignal<WorkflowRunDetail>()
  const [loadError, setLoadError] = createSignal<string | null>(null)
  const action = createWorkflowAction()
  const worker = () => props.runRef.coordinatorWorkerId
  const runId = () => props.runRef.runId
  const nameOf = (id: string) => computerName(props.computers, id)
  let leadAnnounced = false
  let disposed = false
  let generation = 0

  const accept = (next: WorkflowRunDetail | undefined) => {
    if (!next)
      return
    setDetail(next)
    const run = next.run
    if (!leadAnnounced && run?.leadAgentId) {
      leadAnnounced = true
      props.onLeadKnown?.({ agentId: run.leadAgentId, workerId: run.coordinatorWorkerId || worker(), workspaceId: run.workspaceId })
    }
  }

  const refresh = async () => {
    if (disposed)
      return
    const mine = ++generation
    try {
      const resp = await workerRpc.getWorkflowRun(worker(), { runId: runId() })
      if (disposed || mine !== generation)
        return
      accept(resp.detail)
      setLoadError(null)
    }
    catch (err) {
      if (!disposed && mine === generation)
        setLoadError(workflowErrorMessage(err, 'Could not read the run from its coordinator.'))
    }
  }

  /** One user action; its answer is the fresh detail, so the panel never waits for a poll. */
  // Named `run` so the reactivity lint treats its callback as an event body.
  const run = (fn: () => Promise<{ detail?: WorkflowRunDetail | undefined } | void>) => action.run(async () => {
    // A later poll must not paint over the answer this action returns.
    generation++
    const resp = await fn()
    if (resp && resp.detail)
      accept(resp.detail)
    else
      await refresh()
  })

  onMount(() => {
    void refresh()
    const timer = setInterval(() => void refresh(), RUN_POLL_INTERVAL_MS)
    onCleanup(() => clearInterval(timer))
  })
  onCleanup(() => {
    disposed = true
  })

  const currentRun = () => detail()?.run
  const status = () => runStatusView(currentRun()?.status ?? WorkflowRunStatus.UNSPECIFIED)
  const pendingApprovals = createMemo(() => (detail()?.approvals ?? []).filter(a => a.status === WorkflowApprovalStatus.PENDING))
  const decidedApprovals = createMemo(() => (detail()?.approvals ?? []).filter(a => a.status !== WorkflowApprovalStatus.PENDING))
  const events = createMemo(() => [...(detail()?.events ?? [])].reverse())
  const taskTitle = (taskId: string) => detail()?.tasks.find(t => t.id === taskId)?.title ?? taskId
  const slotAgent = (slotId: string) => detail()?.agents.find(a => a.slotId === slotId)

  const controlRun = (runAction: WorkflowRunAction) => void run(async () => {
    const resp = await workerRpc.controlWorkflowRun(worker(), { runId: runId(), action: runAction })
    if (runAction === WorkflowRunAction.DELETE) {
      // Nothing is left to read: stop polling before the panel closes.
      disposed = true
      props.onDeleted?.()
      return resp
    }
    return resp
  })

  return (
    <aside class={panelStyles.panel} aria-label="Workflow run" data-testid="workflow-run-panel">
      <header class={panelStyles.header}>
        <div class={panelStyles.heading}>
          <h2 class={panelStyles.name}>{currentRun()?.name ?? 'Workflow run'}</h2>
          <Show when={currentRun()}>
            <span class={styles.inline}>
              <StatusBadge label={status().label} tone={status().tone} testId="workflow-run-status" />
              <Show when={currentRun()?.statusDetail}>
                <span class={styles.secondary}>{currentRun()?.statusDetail}</span>
              </Show>
            </span>
          </Show>
          <span class={styles.inline}>
            <span class={styles.secondary}>Coordinator</span>
            <ComputerLabel computers={props.computers} workerId={worker()} />
          </span>
        </div>
        <IconButton icon={X} size="sm" onClick={() => props.onClose()} aria-label="Close workflow run" />
      </header>

      <div class={panelStyles.body}>
        <Show when={loadError()}>
          {message => <Alert variant="warning">{message()}</Alert>}
        </Show>
        <Show when={action.error()}>
          {message => <Alert variant="danger">{message()}</Alert>}
        </Show>

        <Show when={currentRun()} fallback={<Show when={!loadError()}><p class={styles.hint}>Loading the run...</p></Show>}>
          {r => (
            <>
              <section class={panelStyles.section} aria-label="Objective">
                <p class={styles.pre}>{r().objective}</p>
                <Show when={r().summary}>
                  <p class={styles.pre}>
                    <strong>Summary: </strong>
                    {r().summary}
                  </p>
                </Show>
                <span class={styles.secondary}>{`${r().workingDir}${r().baseBranch ? ` · from ${r().baseBranch}` : ''}`}</span>
              </section>

              <div class={styles.inline} role="group" aria-label="Run controls">
                <Show when={canPauseRun(r().status)}>
                  <button type="button" class="outline small" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.PAUSE)}>Pause</button>
                </Show>
                <Show when={canResumeRun(r().status)}>
                  <button type="button" class="small" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.RESUME)}>Resume</button>
                </Show>
                <Show when={canCancelRun(r().status)}>
                  <ConfirmButton class="outline small" data-variant="danger" confirmLabel="Cancel the run?" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.CANCEL)}>
                    Cancel
                  </ConfirmButton>
                </Show>
                <button type="button" class="outline small" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.RECONCILE)}>Refresh</button>
                <Show when={canDeleteRun(r().status)}>
                  <ConfirmButton class="outline small" data-variant="danger" confirmLabel="Delete the run?" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.DELETE)}>
                    Delete
                  </ConfirmButton>
                </Show>
              </div>

              <section class={panelStyles.section} aria-label="Approvals">
                <h3 class={panelStyles.sectionTitle}>{`Approvals (${pendingApprovals().length} pending)`}</h3>
                <Show when={pendingApprovals().length > 0} fallback={<p class={styles.hint}>Nothing waits on you.</p>}>
                  <ul class={styles.list}>
                    <For each={pendingApprovals()}>
                      {approval => (
                        <ApprovalCard
                          approval={approval}
                          taskTitle={approval.taskId ? taskTitle(approval.taskId) : ''}
                          busy={action.busy()}
                          onDecide={(approve, note) => void run(() => workerRpc.decideWorkflowApproval(worker(), { runId: runId(), approvalId: approval.id, approve, note }))}
                        />
                      )}
                    </For>
                  </ul>
                </Show>
              </section>

              <section class={panelStyles.section} aria-label="Agents">
                <h3 class={panelStyles.sectionTitle}>Agents</h3>
                <ul class={styles.list}>
                  <For each={detail()?.agents ?? []}>
                    {agent => (
                      <AgentCard
                        agent={agent}
                        role={detail()?.roles.find(a => a.roleKey === agent.roleKey)}
                        isLead={agent.slotId === r().leadSlotId}
                        currentTask={agent.currentTaskId ? taskTitle(agent.currentTaskId) : ''}
                        computers={props.computers}
                        busy={action.busy()}
                        final={isRunFinal(r().status)}
                        onOpen={() => props.onOpenAgent({ agentId: agent.agentId, workerId: agent.workerId, workspaceId: r().workspaceId })}
                        onMessage={text => run(async () => {
                          await workerRpc.sendWorkflowAgentMessage(worker(), { runId: runId(), slotId: agent.slotId, text })
                        })}
                      />
                    )}
                  </For>
                </ul>
              </section>

              <section class={panelStyles.section} aria-label="Roles">
                <h3 class={panelStyles.sectionTitle}>Roles</h3>
                <ul class={styles.list}>
                  <For each={detail()?.roles ?? []}>
                    {assignment => (
                      <RoleCard
                        assignment={assignment}
                        computers={props.computers}
                        nameOf={nameOf}
                        busy={action.busy()}
                        final={isRunFinal(r().status)}
                        onUpdate={draft => run(() => workerRpc.updateWorkflowRole(worker(), {
                          runId: runId(),
                          override: buildRoleUpdate(draft, assignment.role?.isLead ?? false),
                        }))}
                      />
                    )}
                  </For>
                </ul>
              </section>

              <section class={panelStyles.section} aria-label="Tasks">
                <h3 class={panelStyles.sectionTitle}>Tasks</h3>
                <Show when={(detail()?.tasks.length ?? 0) > 0} fallback={<p class={styles.hint}>No tasks yet.</p>}>
                  <ul class={styles.list}>
                    <For each={detail()?.tasks ?? []}>
                      {task => (
                        <TaskCard
                          task={task}
                          computers={props.computers}
                          taskTitle={taskTitle}
                          assignedWorkerId={task.workerId || (task.slotId ? slotAgent(task.slotId)?.workerId : '') || ''}
                          busy={action.busy()}
                          final={isRunFinal(r().status)}
                          onAction={(taskAction, workerId) => void run(() => workerRpc.controlWorkflowTask(worker(), {
                            runId: runId(),
                            taskId: task.id,
                            action: taskAction,
                            workerId: workerId ?? '',
                          }))}
                        />
                      )}
                    </For>
                  </ul>
                </Show>
              </section>

              <Show when={decidedApprovals().length > 0}>
                <section class={panelStyles.section} aria-label="Decided approvals">
                  <h3 class={panelStyles.sectionTitle}>Decided approvals</h3>
                  <ul class={styles.list}>
                    <For each={decidedApprovals()}>
                      {(approval) => {
                        const view = () => approvalStatusView(approval.status)
                        return (
                          <li class={styles.inline}>
                            <StatusBadge label={view().label} tone={view().tone} />
                            <span>{approval.title || approvalKindLabel(approval.kind)}</span>
                            <Show when={approval.decisionNote}>
                              <span class={styles.secondary}>{approval.decisionNote}</span>
                            </Show>
                          </li>
                        )
                      }}
                    </For>
                  </ul>
                </section>
              </Show>

              <section class={panelStyles.section} aria-label="Activity">
                <h3 class={panelStyles.sectionTitle}>Activity</h3>
                <ul class={panelStyles.events}>
                  <For each={events()}>
                    {event => (
                      <li class={panelStyles.event} classList={{ [panelStyles.eventError]: event.kind === WorkflowEventKind.ERROR }}>
                        <span class={styles.secondary}>{formatTime(event.atMs)}</span>
                        <span class={styles.secondary}>{eventKindLabel(event.kind)}</span>
                        <span>{event.message}</span>
                      </li>
                    )}
                  </For>
                </ul>
              </section>
            </>
          )}
        </Show>
      </div>
    </aside>
  )
}

function ApprovalCard(props: {
  approval: WorkflowApproval
  taskTitle: string
  busy: boolean
  onDecide: (approve: boolean, note: string) => void
}) {
  const [note, setNote] = createSignal('')
  return (
    <li class={styles.card} data-testid="workflow-approval">
      <span class={styles.inline}>
        <StatusBadge label={approvalKindLabel(props.approval.kind)} tone="warning" />
        <strong>{props.approval.title}</strong>
      </span>
      <Show when={props.taskTitle}>
        <span class={styles.secondary}>{`Task: ${props.taskTitle}`}</span>
      </Show>
      <Show when={props.approval.details}>
        <p class={styles.pre}>{props.approval.details}</p>
      </Show>
      <Show when={props.approval.requestedBy}>
        <span class={styles.secondary}>{`Requested by ${props.approval.requestedBy}`}</span>
      </Show>
      <input
        type="text"
        aria-label={`Note for ${props.approval.title || 'this approval'}`}
        placeholder="Note (optional)"
        value={note()}
        onInput={e => setNote(e.currentTarget.value)}
      />
      <div class={styles.actions}>
        <button type="button" class="outline small" data-variant="danger" disabled={props.busy} onClick={() => props.onDecide(false, note().trim())}>Reject</button>
        <button type="button" class="small" disabled={props.busy} onClick={() => props.onDecide(true, note().trim())}>Approve</button>
      </div>
    </li>
  )
}

function AgentCard(props: {
  agent: WorkflowRunAgent
  role: WorkflowRoleAssignment | undefined
  isLead: boolean
  currentTask: string
  computers: readonly WorkflowComputer[]
  busy: boolean
  final: boolean
  onOpen: () => void
  onMessage: (text: string) => Promise<boolean>
}) {
  const [composing, setComposing] = createSignal(false)
  const [text, setText] = createSignal('')
  const status = () => agentStatusView(props.agent.status)
  const agentConfig = () => props.role?.role?.agent
  const roleName = () => props.role?.role?.name || props.agent.roleKey
  return (
    <li class={styles.card} data-testid="workflow-agent">
      <span class={styles.inline}>
        <strong>{roleName()}</strong>
        <Show when={props.isLead}>
          <StatusBadge label="Lead" tone="neutral" />
        </Show>
        <StatusBadge label={status().label} tone={status().tone} />
      </span>
      <span class={styles.secondary}>{`${agentProviderLabel(agentConfig()?.agentProvider)} · ${modelSummary(agentConfig()?.options)}`}</span>
      <ComputerLabel computers={props.computers} workerId={props.agent.workerId} />
      <Show when={props.agent.statusDetail}>
        <span class={styles.secondary}>{props.agent.statusDetail}</span>
      </Show>
      <Show when={props.currentTask}>
        <span class={styles.secondary}>{`Working on: ${props.currentTask}`}</span>
      </Show>
      <div class={styles.actions}>
        <Show when={props.agent.agentId}>
          <button type="button" class="outline small" aria-label={`Open chat: ${roleName()}`} onClick={() => props.onOpen()}>Open chat</button>
        </Show>
        <Show when={!props.final}>
          <button type="button" class="outline small" aria-label={`Message ${roleName()}`} onClick={() => setComposing(v => !v)}>Message</button>
        </Show>
      </div>
      <Show when={composing()}>
        <textarea
          class={styles.textarea}
          aria-label={`Message to ${roleName()}`}
          value={text()}
          onInput={e => setText(e.currentTarget.value)}
        />
        <div class={styles.actions}>
          <button type="button" class="outline small" onClick={() => setComposing(false)}>Cancel</button>
          <button
            type="button"
            class="small"
            disabled={props.busy || !text().trim()}
            onClick={() => void props.onMessage(text().trim()).then((ok) => {
              if (ok) {
                setText('')
                setComposing(false)
              }
            })}
          >
            Send
          </button>
        </div>
      </Show>
    </li>
  )
}

function RoleCard(props: {
  assignment: WorkflowRoleAssignment
  computers: readonly WorkflowComputer[]
  nameOf: (workerId: string) => string
  busy: boolean
  final: boolean
  onUpdate: (draft: RoleLaunchDraft) => Promise<boolean>
}) {
  const [editing, setEditing] = createSignal(false)
  const [workerId, setWorkerId] = createSignal('')
  const [model, setModel] = createSignal('')
  const [effort, setEffort] = createSignal('')
  const role = () => props.assignment.role
  const isLead = () => role()?.isLead ?? false
  const roleName = () => role()?.name || props.assignment.roleKey
  return (
    <li class={styles.card} data-testid="workflow-role">
      <span class={styles.inline}>
        <strong>{roleName()}</strong>
        <span class={styles.secondary}>{props.assignment.roleKey}</span>
        <Show when={isLead()}>
          <StatusBadge label="Lead" tone="neutral" />
        </Show>
      </span>
      <span class={styles.secondary}>
        {`${agentProviderLabel(role()?.agent?.agentProvider)} · ${modelSummary(role()?.agent?.options)}`}
      </span>
      <span class={styles.secondary}>
        {isLead() ? 'On the coordinator' : placementSummary(role()?.agent?.placement, props.nameOf)}
        {props.assignment.workerId && !isLead() ? ` · last placed on ${props.nameOf(props.assignment.workerId)}` : ''}
      </span>
      <Show when={!props.final}>
        <div class={styles.actions}>
          <button type="button" class="outline small" onClick={() => setEditing(v => !v)}>
            {isLead() ? 'Change model' : 'Change computer/model'}
          </button>
        </div>
      </Show>
      <Show when={editing()}>
        <p class={styles.hint}>Applies to agents opened from now on; a running agent keeps its settings.</p>
        <Show when={!isLead()}>
          <ComputerPicker
            label={`Computer for ${roleName()}`}
            computers={props.computers}
            value={workerId()}
            emptyLabel="Keep the current placement"
            onChange={setWorkerId}
          />
        </Show>
        <div class={styles.grid2}>
          <input type="text" aria-label={`Model for ${roleName()}`} placeholder="Model (keep)" value={model()} onInput={e => setModel(e.currentTarget.value)} />
          <input type="text" aria-label={`Effort for ${roleName()}`} placeholder="Effort (keep)" value={effort()} onInput={e => setEffort(e.currentTarget.value)} />
        </div>
        <div class={styles.actions}>
          <button type="button" class="outline small" onClick={() => setEditing(false)}>Cancel</button>
          <button
            type="button"
            class="small"
            disabled={props.busy || (!workerId() && !model().trim() && !effort().trim())}
            onClick={() => void props.onUpdate({ roleKey: props.assignment.roleKey, workerId: workerId(), model: model(), effort: effort() }).then((ok) => {
              if (ok)
                setEditing(false)
            })}
          >
            Apply
          </button>
        </div>
      </Show>
    </li>
  )
}

function TaskCard(props: {
  task: WorkflowTask
  computers: readonly WorkflowComputer[]
  taskTitle: (taskId: string) => string
  assignedWorkerId: string
  busy: boolean
  final: boolean
  onAction: (action: WorkflowTaskAction, workerId?: string) => void
}) {
  const [reassignTo, setReassignTo] = createSignal('')
  const status = () => taskStatusView(props.task.status)
  const result = () => props.task.result
  return (
    <li class={styles.card} data-testid="workflow-task" data-task-id={props.task.id}>
      <span class={styles.inline}>
        <strong>{props.task.title}</strong>
        <StatusBadge label={status().label} tone={status().tone} />
      </span>
      <span class={styles.secondary}>
        {`${props.task.roleKey} · attempt ${props.task.attempt}/${props.task.maxAttempts}`}
      </span>
      <Show when={props.task.statusDetail}>
        <span class={styles.secondary}>{props.task.statusDetail}</span>
      </Show>
      <Show when={props.assignedWorkerId}>
        <ComputerLabel computers={props.computers} workerId={props.assignedWorkerId} />
      </Show>
      <Show when={props.task.dependsOn.length > 0}>
        <span class={styles.secondary}>{`After: ${props.task.dependsOn.map(props.taskTitle).join(', ')}`}</span>
      </Show>
      <Show when={props.task.reviewFeedback}>
        <span class={styles.secondary}>{`Review: ${props.task.reviewFeedback}`}</span>
      </Show>
      <Show when={result()}>
        {r => (
          <div class={styles.form} data-testid="workflow-task-result">
            <span class={styles.inline}>
              <StatusBadge label={taskOutcomeView(r().outcome).label} tone={taskOutcomeView(r().outcome).tone} />
              <Show when={r().testsRan}>
                <StatusBadge label={r().testsPassed ? 'Tests passed' : 'Tests failed'} tone={r().testsPassed ? 'success' : 'danger'} />
              </Show>
            </span>
            <Show when={r().summary}>
              <p class={styles.pre}>{r().summary}</p>
            </Show>
            <Show when={r().gitBranch || r().gitCommit}>
              <span class={styles.secondary}>{[r().gitBranch, r().gitCommit.slice(0, 12)].filter(Boolean).join(' @ ')}</span>
            </Show>
            <Show when={r().testSummary}>
              <span class={styles.secondary}>{r().testSummary}</span>
            </Show>
            <Show when={r().artifacts.length > 0}>
              <ul class={styles.list} aria-label="Artifacts">
                <For each={r().artifacts}>{artifact => <li class={styles.pre}>{artifact}</li>}</For>
              </ul>
            </Show>
            <Show when={r().evidence.length > 0}>
              <ul class={styles.list} aria-label="Evidence">
                <For each={r().evidence}>
                  {evidence => (
                    <li class={styles.pre}>
                      <strong>{`${evidence.label}: `}</strong>
                      {evidence.value}
                    </li>
                  )}
                </For>
              </ul>
            </Show>
          </div>
        )}
      </Show>
      <Show when={!props.final}>
        <div class={styles.actions}>
          <Show when={canRetryTask(props.task.status)}>
            <button type="button" class="outline small" disabled={props.busy} onClick={() => props.onAction(WorkflowTaskAction.RETRY)}>Retry</button>
          </Show>
          <Show when={canCancelTask(props.task.status)}>
            <ConfirmButton class="outline small" data-variant="danger" confirmLabel="Cancel task?" disabled={props.busy} onClick={() => props.onAction(WorkflowTaskAction.CANCEL)}>
              Cancel
            </ConfirmButton>
          </Show>
        </div>
        <Show when={canReassignTask(props.task.status)}>
          <div class={styles.inline}>
            <div class={styles.grow}>
              <ComputerPicker
                label={`Reassign ${props.task.title} to`}
                computers={props.computers}
                value={reassignTo()}
                emptyLabel="Reassign to a computer..."
                onChange={setReassignTo}
              />
            </div>
            <button
              type="button"
              class="outline small"
              disabled={props.busy || !reassignTo()}
              onClick={() => props.onAction(WorkflowTaskAction.REASSIGN, reassignTo())}
            >
              Reassign
            </button>
          </div>
        </Show>
      </Show>
    </li>
  )
}
