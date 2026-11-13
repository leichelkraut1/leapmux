import type { TradingCandidate, TradingLabRunView } from '~/generated/proto/leapmux/v1/trading_lab_pb'
import type { WorkflowRunDetail, WorkflowRunSummary } from '~/generated/proto/leapmux/v1/workflow_pb'
import { createEffect, createSignal, For, on, onMount, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { RefreshButton } from '~/components/common/RefreshButton'
import { TradingCandidateStatus } from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { WorkflowRunAction } from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  auditFindings,
  candidatesBySource,
  candidateStatusView,
  evaluationRows,
  isTradingTemplate,
  labelOwner,
  money,
  percent,
  shortHash,
} from '~/stores/tradingLab'
import { runStatusView, taskStatusView, workflowErrorMessage } from '~/stores/workflows'
import { StatusBadge } from '../ComputerPicker'
import { createWorkflowAction } from '../createWorkflowAction'
import * as styles from '../Workflows.css'
import * as t from './Trading.css'

/** The lab's runs on one computer and, for the chosen one, everything it produced. */
export function TradingRuns(props: { workerId: string }) {
  const [runs, setRuns] = createSignal<WorkflowRunSummary[]>([])
  const [selected, setSelected] = createSignal('')
  const [error, setError] = createSignal<string | null>(null)
  const [loading, setLoading] = createSignal(false)

  const load = async () => {
    setLoading(true)
    try {
      const resp = await workerRpc.listWorkflowRuns(props.workerId, {})
      // The list carries each run's template name only, so a run is shown
      // here when its full template proves it a lab run (checked on open).
      setRuns(resp.runs)
      setError(null)
      if (!selected() && resp.runs[0]?.run)
        setSelected(resp.runs[0].run.id)
    }
    catch (err) {
      setError(workflowErrorMessage(err, 'Could not list the runs on that computer.'))
    }
    finally {
      setLoading(false)
    }
  }
  createEffect(on(() => props.workerId, () => void load()))

  return (
    <div class={styles.form}>
      <div class={styles.sectionHeader}>
        <p class={styles.description}>Launch a run from the "Blind trading research" template (Templates, then Add example). Its results appear here as each step freezes them.</p>
        <RefreshButton onClick={() => void load()} disabled={loading()} title="Reload the runs" />
      </div>
      <Show when={error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <div class={styles.chips}>
        <For each={runs()}>
          {r => (
            <button
              type="button"
              class={styles.chip}
              classList={{ outline: selected() !== r.run?.id }}
              onClick={() => setSelected(r.run?.id ?? '')}
              data-testid="trading-run-chip"
            >
              {`${r.run?.name ?? ''} · ${new Date(Number(r.run?.createdAtMs ?? 0)).toLocaleString()}`}
            </button>
          )}
        </For>
      </div>
      <Show when={runs().length === 0 && !loading()}>
        <div class={styles.empty}><strong>No runs on this computer</strong></div>
      </Show>
      <Show when={selected()} keyed>
        {runId => <TradingRunView workerId={props.workerId} runId={runId} />}
      </Show>
    </div>
  )
}

function TradingRunView(props: { workerId: string, runId: string }) {
  const [detail, setDetail] = createSignal<WorkflowRunDetail>()
  const [view, setView] = createSignal<TradingLabRunView>()
  const [error, setError] = createSignal<string | null>(null)
  const action = createWorkflowAction()

  const load = async () => {
    try {
      const [d, v] = await Promise.all([
        workerRpc.getWorkflowRun(props.workerId, { runId: props.runId }),
        workerRpc.getTradingLabRun(props.workerId, { runId: props.runId }),
      ])
      setDetail(d.detail)
      setView(v.view)
      setError(null)
    }
    catch (err) {
      setError(workflowErrorMessage(err, 'Could not read the run.'))
    }
  }
  onMount(() => void load())

  const isLab = () => isTradingTemplate(detail()?.run?.template)
  const bySource = () => candidatesBySource(view()?.candidates ?? [])
  const status = () => runStatusView(detail()?.run?.status ?? 0)
  const controlRun = (act: WorkflowRunAction) => void action.run(async () => {
    const resp = await workerRpc.controlWorkflowRun(props.workerId, { runId: props.runId, action: act })
    setDetail(resp.detail)
  })
  const decide = (c: TradingCandidate, approve: boolean) => void action.run(async () => {
    await workerRpc.decideTradingCandidate(props.workerId, { candidateId: c.id, approve })
    await load()
  })

  return (
    <div class={styles.form} data-testid="trading-run-view">
      <Show when={error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={action.error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={detail() && !isLab()}>
        <Alert>This run is not a trading research run.</Alert>
      </Show>
      <Show when={detail()?.run}>
        {run => (
          <div class={styles.card}>
            <div class={styles.row}>
              <div class={styles.identity}>
                <span class={styles.title}>{run().name}</span>
                <span class={styles.secondary}>{run().statusDetail || run().summary}</span>
              </div>
              <StatusBadge label={status().label} tone={status().tone} testId="trading-run-status" />
              <RefreshButton onClick={() => void load()} disabled={action.busy()} title="Reload the run" />
            </div>
            <div class={styles.actions}>
              <button type="button" class="outline" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.PAUSE)}>Pause</button>
              <button type="button" class="outline" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.RESUME)}>Resume</button>
              <button type="button" class="outline" disabled={action.busy()} onClick={() => controlRun(WorkflowRunAction.CANCEL)}>Cancel run</button>
            </div>
            <div class={t.tableWrap}>
              <table class={t.table} aria-label="Run progress">
                <tbody>
                  <For each={detail()?.tasks ?? []}>
                    {(task) => {
                      const v = () => taskStatusView(task.status)
                      const frozen = () => detail()?.frozenOutputs.find(f => f.taskId === task.id)
                      return (
                        <tr>
                          <td class={t.cell}>{task.title}</td>
                          <td class={t.cell}><StatusBadge label={v().label} tone={v().tone} /></td>
                          <td class={t.wrapCell}>{task.statusDetail}</td>
                          <td class={t.cell}>
                            <Show when={frozen()}>{f => <span class={t.mono}>{`frozen ${shortHash(f().sha256)}`}</span>}</Show>
                          </td>
                        </tr>
                      )
                    }}
                  </For>
                </tbody>
              </table>
            </div>
          </div>
        )}
      </Show>

      <Show when={view()?.snapshot}>
        {snap => (
          <div class={styles.card} data-testid="trading-snapshot">
            <span class={styles.title}>Market snapshot</span>
            <span class={styles.secondary}>
              {`${snap().symbols.join(', ')} · ${snap().contracts} contracts · ${snap().catalysts} catalysts · as of ${new Date(Number(snap().asOfMs)).toLocaleString()}`}
            </span>
            <span class={t.mono}>{`sha256 ${snap().sha256}`}</span>
            <Show when={snap().warnings.length > 0}>
              <ul class={t.reasons}><For each={snap().warnings}>{w => <li>{w}</li>}</For></ul>
            </Show>
          </div>
        )}
      </Show>

      <Show when={view()?.recorded}>
        <div class={t.sideBySide} data-testid="trading-side-by-side">
          <CandidateColumn title="Blind analyst" sha={view()?.blindPredictionSha256 ?? ''} candidates={bySource().blind} busy={action.busy()} onDecide={decide} />
          <CandidateColumn title="Contrarian analyst" sha={view()?.contrarianPredictionSha256 ?? ''} candidates={bySource().contrarian} busy={action.busy()} onDecide={decide} />
        </div>
        <EvaluationTable view={view()!} />
        <AuditTable view={view()!} />
      </Show>
      <Show when={view() && !view()?.recorded && isLab()}>
        <div class={styles.empty}>
          <strong>Not recorded yet</strong>
          <p>The predictions stay hidden here until the evaluation and the audit are frozen and the record step books them.</p>
        </div>
      </Show>
    </div>
  )
}

function CandidateColumn(props: { title: string, sha: string, candidates: TradingCandidate[], busy: boolean, onDecide: (c: TradingCandidate, approve: boolean) => void }) {
  return (
    <div class={t.column}>
      <span class={styles.title}>{props.title}</span>
      <span class={t.mono}>{`prediction sha256 ${shortHash(props.sha)}`}</span>
      <For each={props.candidates}>
        {(c) => {
          const v = () => candidateStatusView(c.status)
          return (
            <div class={styles.card} data-testid="trading-candidate">
              <div class={styles.row}>
                <div class={styles.identity}>
                  <span class={styles.title}>{c.status === TradingCandidateStatus.NO_TRADE ? 'No trade' : `${c.strategy.replace('_', ' ')} ${c.occSymbol}`}</span>
                  <span class={styles.secondary}>{c.anonymousLabel ? `Scored as ${c.anonymousLabel}` : ''}</span>
                </div>
                <StatusBadge label={v().label} tone={v().tone} />
              </div>
              <Show when={c.status !== TradingCandidateStatus.NO_TRADE}>
                <div class={t.ticketGrid}>
                  <span>Entry limit</span>
                  <span>{money(c.entryLimit)}</span>
                  <span>Max loss / contract</span>
                  <span>{money(c.maxLossPerContract)}</span>
                  <span>Catalyst</span>
                  <span>{c.catalyst}</span>
                  <span>P(profit) · 3x · 5x</span>
                  <span>{`${percent(c.probabilityProfit, 0)} · ${percent(c.probability3x, 0)} · ${percent(c.probability5x, 0)}`}</span>
                  <span>Evaluation</span>
                  <span>{c.evaluationVerdict ? `${c.evaluationScore.toFixed(1)} / 100 (${c.evaluationVerdict})` : 'not scored'}</span>
                  <span>Audit</span>
                  <span>{c.auditVerdict || 'not audited'}</span>
                </div>
              </Show>
              <p class={styles.hint}>{c.thesis}</p>
              <Show when={c.rejectionReasons.length > 0}>
                <ul class={t.reasons}><For each={c.rejectionReasons}>{r => <li>{r}</li>}</For></ul>
              </Show>
              <Show when={c.status === TradingCandidateStatus.ELIGIBLE}>
                <div class={styles.actions}>
                  <button type="button" class="outline" disabled={props.busy} onClick={() => props.onDecide(c, false)}>Dismiss</button>
                  <button type="button" disabled={props.busy} onClick={() => props.onDecide(c, true)} data-testid="trading-approve-candidate">Add to approved queue</button>
                </div>
              </Show>
            </div>
          )
        }}
      </For>
    </div>
  )
}

const CRITERIA = ['evidence', 'pricing', 'catalyst', 'risk_reward', 'calibration', 'executability']

function EvaluationTable(props: { view: TradingLabRunView }) {
  const rows = () => evaluationRows(props.view.evaluationJson)
  return (
    <div class={styles.card} data-testid="trading-evaluation">
      <span class={styles.title}>Anonymous evaluation</span>
      <p class={styles.hint}>The evaluator saw each submission under its label only. Totals are the lab's, from these scores by one rubric.</p>
      <div class={t.tableWrap}>
        <table class={t.table}>
          <thead>
            <tr>
              <th class={t.cell}>Submission</th>
              <th class={t.cell}>Whose</th>
              <th class={t.cell}>Idea</th>
              <For each={CRITERIA}>{c => <th class={t.numeric}>{c.replace('_', ' ')}</th>}</For>
              <th class={t.cell}>Verdict</th>
              <th class={t.cell}>Notes</th>
            </tr>
          </thead>
          <tbody>
            <For each={rows()}>
              {r => (
                <tr>
                  <td class={t.cell}>{r.label}</td>
                  <td class={t.cell}>{labelOwner(props.view.labelSources, r.label)}</td>
                  <td class={t.cell}>{r.recommendationId}</td>
                  <For each={CRITERIA}>{c => <td class={t.numeric}>{r.scores[c] ?? '-'}</td>}</For>
                  <td class={t.cell}>{r.verdict}</td>
                  <td class={t.wrapCell}>{r.notes}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
    </div>
  )
}

function AuditTable(props: { view: TradingLabRunView }) {
  const rows = () => auditFindings(props.view.auditJson)
  return (
    <div class={styles.card} data-testid="trading-audit">
      <span class={styles.title}>Risk audit findings</span>
      <div class={t.tableWrap}>
        <table class={t.table}>
          <thead>
            <tr>
              <th class={t.cell}>Submission</th>
              <th class={t.cell}>Idea</th>
              <th class={t.cell}>Verdict</th>
              <th class={t.cell}>Failed checks</th>
              <th class={t.cell}>Issues</th>
            </tr>
          </thead>
          <tbody>
            <For each={rows()}>
              {r => (
                <tr>
                  <td class={t.cell}>{r.label}</td>
                  <td class={t.cell}>{r.recommendationId}</td>
                  <td class={t.cell}>{r.verdict}</td>
                  <td class={t.wrapCell}>{r.failedChecks.join(', ') || 'none'}</td>
                  <td class={t.wrapCell}>{r.issues.join('; ') || 'none'}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
    </div>
  )
}
