import type { GetTradingDashboardResponse, TradingCandidate, TradingOrder, TradingPosition } from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { RefreshButton } from '~/components/common/RefreshButton'
import { showInfoToast } from '~/components/common/Toast'
import {
  TradingBook,
  TradingCandidateStatus,
  TradingLabMode,
  TradingOrderSide,
  TradingOrderState,
  TradingPositionState,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { BOOK_LABELS, candidateStatusView, money, orderStateView, secondsToConfirm, shortHash, sideLabel } from '~/stores/tradingLab'
import { workflowErrorMessage } from '~/stores/workflows'
import { StatusBadge } from '../ComputerPicker'
import { createWorkflowAction } from '../createWorkflowAction'
import * as styles from '../Workflows.css'
import * as t from './Trading.css'

/**
 * The approved trade queue, the execution gateway and every order. Preparing
 * runs the gateway's checks and the broker's preview and places nothing; the
 * ticket that follows shows the exact order, and only approving it in this
 * browser before it expires submits it. The worker accepts that approval from
 * your browser session alone, never from an agent or an app.
 */
export function TradingOrders(props: { workerId: string }) {
  const [dash, setDash] = createSignal<GetTradingDashboardResponse>()
  const [error, setError] = createSignal<string | null>(null)
  const [ticket, setTicket] = createSignal<TradingOrder>()
  const action = createWorkflowAction()

  const load = async () => {
    try {
      setDash(await workerRpc.getTradingDashboard(props.workerId, {}))
      setError(null)
    }
    catch (err) {
      setError(workflowErrorMessage(err, 'Could not read the lab.'))
    }
  }
  createEffect(on(() => props.workerId, () => void load()))

  const live = () => dash()?.settings?.mode === TradingLabMode.LIVE
  const openLive = () => (dash()?.positions ?? []).filter(p => p.book === TradingBook.LIVE && p.state === TradingPositionState.OPEN)

  return (
    <div class={styles.form} data-testid="trading-orders">
      <Show when={error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={action.error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={dash() && !live()}>
        <Alert>Paper mode: the gateway places no live orders. Switch to live mode in Settings after verifying the broker.</Alert>
      </Show>

      <Show when={ticket()}>
        {order => (
          <OrderTicket
            workerId={props.workerId}
            order={order()}
            onDone={(o) => {
              setTicket(undefined)
              if (o)
                showInfoToast(`Order ${orderStateView(o.state).label.toLowerCase()}.`)
              void load()
            }}
          />
        )}
      </Show>

      <div class={styles.card}>
        <div class={styles.sectionHeader}>
          <span class={styles.title}>Approved trade queue</span>
          <RefreshButton onClick={() => void load()} disabled={action.busy()} title="Reload" />
        </div>
        <Show when={(dash()?.queue ?? []).length === 0}>
          <p class={styles.hint}>Nothing is queued. Approve an eligible idea from a research run first.</p>
        </Show>
        <For each={dash()?.queue ?? []}>
          {c => (
            <QueueRow
              candidate={c}
              live={live()}
              busy={action.busy()}
              onPrepare={(qty, limit) => void action.run(async () => {
                const resp = await workerRpc.prepareTradingOrder(props.workerId, { candidateId: c.id, quantity: qty, limitPrice: limit })
                setTicket(resp.order)
              })}
              onDismiss={() => void action.run(async () => {
                await workerRpc.decideTradingCandidate(props.workerId, { candidateId: c.id, approve: false })
                await load()
              })}
            />
          )}
        </For>
      </div>

      <Show when={openLive().length > 0}>
        <div class={styles.card}>
          <span class={styles.title}>Open live positions</span>
          <For each={openLive()}>
            {p => (
              <ClosePositionRow
                position={p}
                busy={action.busy()}
                onPrepare={(qty, limit) => void action.run(async () => {
                  const resp = await workerRpc.prepareTradingOrder(props.workerId, { positionId: p.id, quantity: qty, limitPrice: limit })
                  setTicket(resp.order)
                })}
              />
            )}
          </For>
        </div>
      </Show>

      <div class={styles.card}>
        <div class={styles.sectionHeader}>
          <span class={styles.title}>Orders</span>
          <button
            type="button"
            class="outline"
            disabled={action.busy()}
            onClick={() => void action.run(async () => {
              await workerRpc.reconcileTradingOrders(props.workerId, {})
              await load()
            })}
            data-testid="trading-reconcile"
          >
            Reconcile with broker
          </button>
        </div>
        <div class={t.tableWrap}>
          <table class={t.table} aria-label="Orders">
            <thead>
              <tr>
                <th class={t.cell}>Status</th>
                <th class={t.cell}>Side</th>
                <th class={t.cell}>Contract</th>
                <th class={t.numeric}>Qty</th>
                <th class={t.numeric}>Limit</th>
                <th class={t.numeric}>Filled</th>
                <th class={t.cell}>Authorized by</th>
                <th class={t.cell}>Broker id</th>
                <th class={t.cell}>Detail</th>
                <th class={t.cell} />
              </tr>
            </thead>
            <tbody>
              <For each={dash()?.orders ?? []}>
                {(o) => {
                  const v = () => orderStateView(o.state)
                  const cancellable = () => [TradingOrderState.PREPARED, TradingOrderState.SUBMITTED, TradingOrderState.PARTIALLY_FILLED].includes(o.state)
                  return (
                    <tr data-testid="trading-order-row">
                      <td class={t.cell}><StatusBadge label={v().label} tone={v().tone} /></td>
                      <td class={t.cell}>{sideLabel(o.side)}</td>
                      <td class={t.cell}>{o.occSymbol}</td>
                      <td class={t.numeric}>{o.quantity}</td>
                      <td class={t.numeric}>{money(o.limitPrice)}</td>
                      <td class={t.numeric}>{o.filledQuantity ? `${o.filledQuantity} @ ${money(o.averageFillPrice)}` : '-'}</td>
                      <td class={t.cell}>{o.confirmedBy || '-'}</td>
                      <td class={t.cell}><span class={t.mono}>{o.brokerOrderId || '-'}</span></td>
                      <td class={t.wrapCell}>{o.detail}</td>
                      <td class={t.cell}>
                        <Show when={o.state === TradingOrderState.PREPARED}>
                          <button type="button" class="outline small" onClick={() => setTicket(o)}>Review</button>
                        </Show>
                        <Show when={cancellable()}>
                          <button
                            type="button"
                            class="outline small"
                            disabled={action.busy()}
                            onClick={() => void action.run(async () => {
                              await workerRpc.cancelTradingOrder(props.workerId, { orderId: o.id })
                              await load()
                            })}
                          >
                            Cancel
                          </button>
                        </Show>
                      </td>
                    </tr>
                  )
                }}
              </For>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}

function QueueRow(props: { candidate: TradingCandidate, live: boolean, busy: boolean, onPrepare: (qty: number, limit: number) => void, onDismiss: () => void }) {
  // eslint-disable-next-line solid/reactivity -- the candidate's limit seeds the field once
  const [limit, setLimit] = createSignal(props.candidate.entryLimit.toFixed(2))
  const [qty, setQty] = createSignal('1')
  const v = () => candidateStatusView(props.candidate.status)
  const approved = () => props.candidate.status === TradingCandidateStatus.APPROVED
  return (
    <div class={styles.row} data-testid="trading-queue-row">
      <div class={styles.identity}>
        <span class={styles.title}>{`${props.candidate.strategy.replace('_', ' ')} ${props.candidate.occSymbol}`}</span>
        <span class={styles.secondary}>{`${BOOK_LABELS[props.candidate.source]} · score ${props.candidate.evaluationScore.toFixed(1)} · max loss ${money(props.candidate.maxLossPerContract)} per contract`}</span>
      </div>
      <StatusBadge label={v().label} tone={v().tone} />
      <Show when={approved()}>
        <label class={styles.field}>
          Contracts
          <input type="number" min="1" step="1" value={qty()} onInput={e => setQty(e.currentTarget.value)} />
        </label>
        <label class={styles.field}>
          Limit
          <input type="number" min="0.01" step="0.01" value={limit()} onInput={e => setLimit(e.currentTarget.value)} />
        </label>
        <button type="button" disabled={props.busy || !props.live} onClick={() => props.onPrepare(Number(qty()), Number(limit()))} data-testid="trading-prepare-order">
          Prepare order
        </button>
      </Show>
      <button type="button" class="outline" disabled={props.busy} onClick={() => props.onDismiss()}>Dismiss</button>
    </div>
  )
}

function ClosePositionRow(props: { position: TradingPosition, busy: boolean, onPrepare: (qty: number, limit: number) => void }) {
  // eslint-disable-next-line solid/reactivity -- the position's size seeds the field once
  const [qty, setQty] = createSignal(String(props.position.quantity))
  const [limit, setLimit] = createSignal('')
  return (
    <div class={styles.row}>
      <div class={styles.identity}>
        <span class={styles.title}>{props.position.occSymbol}</span>
        <span class={styles.secondary}>{`${props.position.quantity} held at ${money(props.position.entryPrice)} · cost ${money(props.position.cost)}`}</span>
      </div>
      <label class={styles.field}>
        Contracts
        <input type="number" min="1" step="1" value={qty()} onInput={e => setQty(e.currentTarget.value)} />
      </label>
      <label class={styles.field}>
        Limit
        <input type="number" min="0.01" step="0.01" value={limit()} onInput={e => setLimit(e.currentTarget.value)} />
      </label>
      <button type="button" class="outline" disabled={props.busy || !limit()} onClick={() => props.onPrepare(Number(qty()), Number(limit()))}>Prepare closing order</button>
    </div>
  )
}

/** The exact order, its checks and the broker's alerts, and the approval. */
function OrderTicket(props: { workerId: string, order: TradingOrder, onDone: (o?: TradingOrder) => void }) {
  const [now, setNow] = createSignal(Date.now())
  const timer = setInterval(() => setNow(Date.now()), 1000)
  onCleanup(() => clearInterval(timer))
  const [asking, setAsking] = createSignal(false)
  const action = createWorkflowAction()
  const summary = () => `${sideLabel(props.order.side)} ${props.order.quantity} × ${props.order.occSymbol} at a limit of ${money(props.order.limitPrice)}`
  const left = () => secondsToConfirm(props.order, now())

  return (
    <div class={t.orderTicket} data-testid="trading-order-ticket" role="group" aria-label="Confirm order">
      <span class={styles.title}>Confirm this exact order</span>
      <div class={t.ticketGrid}>
        <span>Action</span>
        <strong>{`${sideLabel(props.order.side)} ${props.order.quantity} × ${props.order.occSymbol}`}</strong>
        <span>Order</span>
        <span>{`Limit ${money(props.order.limitPrice)}, ${props.order.timeInForce.toUpperCase()}`}</span>
        <span>Quote</span>
        <span>{`${money(props.order.bid)} × ${money(props.order.ask)}`}</span>
        <Show when={props.order.side === TradingOrderSide.BUY_TO_OPEN}>
          <span>Maximum loss</span>
          <strong>{money(props.order.maxLoss)}</strong>
        </Show>
        <span>Estimated fees</span>
        <span>{money(props.order.estimatedFees)}</span>
        <span>Account</span>
        <span>{props.order.accountId.replace(/.(?=.{4})/g, '*')}</span>
        <span>Fingerprint</span>
        <span class={t.mono}>{props.order.fingerprint}</span>
        <span>Expires in</span>
        <span>{left() > 0 ? `${left()} s` : 'expired: prepare it again'}</span>
      </div>
      <Show when={props.order.brokerAlerts.length > 0}>
        <Alert variant="warning">
          <ul class={t.reasons}><For each={props.order.brokerAlerts}>{a => <li>{a}</li>}</For></ul>
        </Alert>
      </Show>
      <details>
        <summary>{`Checks run (${props.order.checks.length})`}</summary>
        <ul class={t.reasons}><For each={props.order.checks}>{c => <li>{c}</li>}</For></ul>
      </details>
      <Show when={action.error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={asking()}>
        <ConfirmDialog
          title="Approve this order?"
          confirmLabel="Approve and place"
          busy={action.busy()}
          onCancel={() => setAsking(false)}
          onConfirm={() => void action.run(async () => {
            const resp = await workerRpc.confirmTradingOrder(props.workerId, {
              orderId: props.order.id,
              fingerprint: props.order.fingerprint,
            })
            setAsking(false)
            props.onDone(resp.order)
          })}
          data-testid="trading-approve-dialog"
          confirmTestId="trading-approve-order"
        >
          <p><strong>{summary()}</strong></p>
          <Show when={props.order.side === TradingOrderSide.BUY_TO_OPEN}>
            <p>{`You can lose up to ${money(props.order.maxLoss)}, plus about ${money(props.order.estimatedFees)} in fees.`}</p>
          </Show>
          <p>{`This places a real order with your broker (order ${shortHash(props.order.fingerprint)}).`}</p>
        </ConfirmDialog>
      </Show>
      <div class={styles.actions}>
        <button type="button" class="outline" onClick={() => props.onDone()}>Not now</button>
        <button
          type="button"
          class="outline"
          disabled={action.busy()}
          onClick={() => void action.run(async () => {
            await workerRpc.cancelTradingOrder(props.workerId, { orderId: props.order.id })
            props.onDone()
          })}
        >
          Withdraw
        </button>
        <button
          type="button"
          data-variant="danger"
          disabled={action.busy() || left() <= 0}
          onClick={() => setAsking(true)}
          data-testid="trading-confirm-order"
        >
          Place this order…
        </button>
      </div>
    </div>
  )
}
