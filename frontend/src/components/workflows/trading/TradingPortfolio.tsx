import type { GetTradingDashboardResponse, TradingBookMetrics } from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { createEffect, createSignal, For, on, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { RefreshButton } from '~/components/common/RefreshButton'
import { TradingBook } from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { BOOK_LABELS, money, percent, positionPnl, positionStateView, splitMetrics } from '~/stores/tradingLab'
import { workflowErrorMessage } from '~/stores/workflows'
import { StatusBadge } from '../ComputerPicker'
import { createWorkflowAction } from '../createWorkflowAction'
import * as styles from '../Workflows.css'
import * as t from './Trading.css'

/**
 * The paper and live performance dashboard. The three simulated books are
 * shown apart from the live one, which holds real fills only.
 */
export function TradingPortfolio(props: { workerId: string }) {
  const [dash, setDash] = createSignal<GetTradingDashboardResponse>()
  const [error, setError] = createSignal<string | null>(null)
  const [warnings, setWarnings] = createSignal<string[]>([])
  const action = createWorkflowAction()
  const [book, setBook] = createSignal<TradingBook | 0>(0)

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
  const metrics = () => splitMetrics(dash()?.metrics ?? [])
  const positions = () => (dash()?.positions ?? []).filter(p => !book() || p.book === book())

  return (
    <div class={styles.form} data-testid="trading-portfolio">
      <Show when={error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={action.error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={warnings().length > 0}>
        <Alert variant="warning"><ul class={t.reasons}><For each={warnings()}>{w => <li>{w}</li>}</For></ul></Alert>
      </Show>
      <div class={styles.sectionHeader}>
        <p class={styles.description}>
          Every book fills at the recorded ask plus slippage, sells at the bid less slippage, pays the same fees and is sized by the same allocation. Marks use the bid; an idea the quote could not fill counts as not executable.
        </p>
        <div class={styles.actions}>
          <button
            type="button"
            class="outline"
            disabled={action.busy()}
            onClick={() => void action.run(async () => {
              const resp = await workerRpc.markTradingBooks(props.workerId, {})
              setWarnings(resp.warnings)
              await load()
            })}
            data-testid="trading-mark-books"
          >
            Mark books now
          </button>
          <RefreshButton onClick={() => void load()} disabled={action.busy()} title="Reload" />
        </div>
      </div>

      <span class={styles.title}>Paper (simulated)</span>
      <div class={t.metricGrid}>
        <For each={metrics().paper}>{m => <BookCard metrics={m} />}</For>
      </div>
      <span class={styles.title}>Live (real fills, reconciled from the broker)</span>
      <Show when={metrics().live} fallback={<p class={styles.hint}>No live positions.</p>}>
        {m => <div class={t.metricGrid}><BookCard metrics={m()} /></div>}
      </Show>

      <div class={styles.card}>
        <div class={styles.sectionHeader}>
          <span class={styles.title}>Positions</span>
          <div class={styles.chips}>
            <For each={[0, TradingBook.BLIND, TradingBook.CONTRARIAN, TradingBook.BLIND_INVERSE, TradingBook.LIVE] as const}>
              {b => (
                <button type="button" class={styles.chip} classList={{ outline: book() !== b }} onClick={() => setBook(b)}>
                  {b ? BOOK_LABELS[b] : 'All books'}
                </button>
              )}
            </For>
          </div>
        </div>
        <div class={t.tableWrap}>
          <table class={t.table} aria-label="Positions">
            <thead>
              <tr>
                <th class={t.cell}>Book</th>
                <th class={t.cell}>Contract</th>
                <th class={t.cell}>State</th>
                <th class={t.numeric}>Qty</th>
                <th class={t.numeric}>Entry</th>
                <th class={t.numeric}>Cost</th>
                <th class={t.numeric}>P/L</th>
                <th class={t.numeric}>Peak</th>
                <th class={t.cell}>Horizons</th>
                <th class={t.cell}>Note</th>
              </tr>
            </thead>
            <tbody>
              <For each={positions()}>
                {(p) => {
                  const v = () => positionStateView(p.state)
                  const pnl = () => positionPnl(p)
                  return (
                    <tr data-testid="trading-position-row">
                      <td class={t.cell}>{BOOK_LABELS[p.book]}</td>
                      <td class={t.cell}>{p.occSymbol}</td>
                      <td class={t.cell}><StatusBadge label={v().label} tone={v().tone} /></td>
                      <td class={t.numeric}>{p.quantity}</td>
                      <td class={t.numeric}>{p.quantity ? money(p.entryPrice) : '-'}</td>
                      <td class={t.numeric}>{p.quantity ? money(p.cost) : '-'}</td>
                      <td class={t.numeric} classList={{ [t.positive]: pnl() > 0, [t.negative]: pnl() < 0 }}>{p.quantity ? money(pnl()) : '-'}</td>
                      <td class={t.numeric}>{p.peakMultiple ? `${p.peakMultiple.toFixed(2)}x` : '-'}</td>
                      <td class={t.cell}>{p.horizons.map(h => `${h.days}d ${money(h.pnl)}`).join(' · ') || '-'}</td>
                      <td class={t.wrapCell}>{p.note}</td>
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

function BookCard(props: { metrics: TradingBookMetrics }) {
  const m = () => props.metrics
  const horizons = () => Object.entries(m().pnlAtHorizon).sort(([a], [b]) => Number(a) - Number(b))
  return (
    <div class={styles.card} data-testid="trading-book-card">
      <span class={styles.title}>{BOOK_LABELS[m().book]}</span>
      <div class={t.metric}>
        <span class={styles.secondary}>Net P/L</span>
        <span class={t.metricValue} classList={{ [t.positive]: m().netPnl > 0, [t.negative]: m().netPnl < 0 }}>{money(m().netPnl)}</span>
      </div>
      <div class={t.ticketGrid}>
        <span>Ideas · filled · not executable · open</span>
        <span>{`${m().ideas} · ${m().filled} · ${m().notExecutable} · ${m().open}`}</span>
        <span>Risk deployed</span>
        <span>{money(m().riskDeployed)}</span>
        <span>Return on risk</span>
        <span>{percent(m().returnOnRisk)}</span>
        <span>Risk-adjusted return</span>
        <span>{m().riskAdjustedReturn.toFixed(2)}</span>
        <span>Win rate</span>
        <span>{percent(m().winRate)}</span>
        <span>Max drawdown</span>
        <span>{money(m().maxDrawdown)}</span>
        <span>Calibration (Brier)</span>
        <span>{m().brierScore ? m().brierScore.toFixed(3) : '-'}</span>
        <span>Reached 3x · 5x</span>
        <span>{`${percent(m().hit3xRate)} · ${percent(m().hit5xRate)}`}</span>
        <For each={horizons()}>
          {([d, v]) => (
            <>
              <span>{`P/L at ${d} days`}</span>
              <span>{money(v)}</span>
            </>
          )}
        </For>
      </div>
    </div>
  )
}
