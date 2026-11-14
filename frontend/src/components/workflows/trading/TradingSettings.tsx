import type { TradingBrokerStatus, TradingLabSettings } from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { clone } from '@bufbuild/protobuf'
import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { PillGroup } from '~/components/common/PillGroup'
import { showInfoToast } from '~/components/common/Toast'
import {
  TradingBrokerAuth,
  TradingLabMode,
  TradingLabSettingsSchema,
  TradingMarketDataSource,
  TradingMcpTransport,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { workflowErrorMessage } from '~/stores/workflows'
import { StatusBadge } from '../ComputerPicker'
import { createWorkflowAction } from '../createWorkflowAction'
import * as styles from '../Workflows.css'
import * as t from './Trading.css'

const MODES = [
  { key: TradingLabMode.PAPER, label: 'Paper only' },
  { key: TradingLabMode.LIVE, label: 'Live (confirmed orders)' },
] as const

const SOURCES = [
  { key: TradingMarketDataSource.REPLAY, label: 'Recorded files' },
  { key: TradingMarketDataSource.ROBINHOOD, label: 'Robinhood MCP (read-only)' },
] as const

const AUTHS = [
  { key: TradingBrokerAuth.SIGN_IN, label: 'Sign in (Robinhood agent server)' },
  { key: TradingBrokerAuth.TOKEN, label: 'Token on this computer' },
] as const

/** Robinhood's MCP server for agents, which the sign-in reaches. */
const ROBINHOOD_AGENT_URL = 'https://agent.robinhood.com/mcp/trading'

/** An unset value is a token, as the worker reads it. */
function authOf(s: TradingLabSettings): TradingBrokerAuth {
  return s.robinhood?.auth === TradingBrokerAuth.SIGN_IN ? TradingBrokerAuth.SIGN_IN : TradingBrokerAuth.TOKEN
}

const TRANSPORTS = [
  { key: TradingMcpTransport.HTTP, label: 'HTTP' },
  { key: TradingMcpTransport.STDIO, label: 'Local command' },
] as const

/**
 * The lab's settings, the broker sign-in and verification, and the read-only
 * connector. No broker token is entered here: the sign-in happens on the
 * broker's own page and the worker keeps the tokens, or the settings name the
 * environment variable or file on the lab computer that holds one.
 */
export function TradingSettings(props: { workerId: string }) {
  const [draft, setDraft] = createSignal<TradingLabSettings>()
  const [broker, setBroker] = createSignal<TradingBrokerStatus>()
  const [error, setError] = createSignal<string | null>(null)
  const [connectorPass, setConnectorPass] = createSignal('')
  const action = createWorkflowAction()

  const load = async () => {
    try {
      const [s, d] = await Promise.all([
        workerRpc.getTradingLabSettings(props.workerId, {}),
        workerRpc.getTradingDashboard(props.workerId, {}),
      ])
      setDraft(s.settings)
      setBroker(d.broker)
      setError(null)
    }
    catch (err) {
      setError(workflowErrorMessage(err, 'Could not read the lab settings.'))
    }
  }
  createEffect(on(() => props.workerId, () => void load()))
  // The broker sign-in finishes in another tab; coming back re-reads it.
  const onFocus = () => void load()
  window.addEventListener('focus', onFocus)
  onCleanup(() => window.removeEventListener('focus', onFocus))

  /** Edits a copy, so a refused save leaves the stored settings as they were. */
  const edit = (fn: (s: TradingLabSettings) => void) => {
    const current = draft()
    if (!current)
      return
    const next = clone(TradingLabSettingsSchema, current)
    fn(next)
    setDraft(next)
  }
  const num = (v: string) => (Number.isFinite(Number(v)) ? Number(v) : 0)

  const save = () => void action.run(async () => {
    const resp = await workerRpc.saveTradingLabSettings(props.workerId, {
      settings: draft(),
      newConnectorPassphrase: connectorPass(),
    })
    setDraft(resp.settings)
    setConnectorPass('')
    showInfoToast('Saved the lab settings.')
  })

  const [signInUrl, setSignInUrl] = createSignal<string>()
  /**
   * Saves the settings, then opens the broker's sign-in page. The tab is
   * opened during the click, so a popup blocker lets it through, and pointed
   * at the page once the worker answers.
   */
  const signIn = () => {
    setSignInUrl(undefined)
    const tab = window.open('', '_blank')
    void action.run(async () => {
      try {
        await workerRpc.saveTradingLabSettings(props.workerId, { settings: draft() })
        const resp = await workerRpc.startTradingBrokerSignIn(props.workerId, {})
        if (tab) {
          tab.opener = null
          tab.location.href = resp.authorizeUrl
        }
        else {
          setSignInUrl(resp.authorizeUrl)
        }
      }
      catch (err) {
        tab?.close()
        throw err
      }
    })
  }

  return (
    <div class={styles.form} data-testid="trading-settings">
      <Show when={error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={action.error()}>{message => <Alert variant="danger">{message()}</Alert>}</Show>
      <Show when={draft()}>
        {s => (
          <>
            <fieldset class={styles.fieldset}>
              <legend>Mode</legend>
              <PillGroup<TradingLabMode> label="Trading mode" options={MODES} selectedKey={s().mode} onSelect={mode => edit(x => (x.mode = mode))} small />
              <p class={styles.hint}>Live mode needs a broker verified as ready. Even then, every order waits for you to approve it here, in your browser; an agent or app cannot.</p>
            </fieldset>

            <fieldset class={styles.fieldset}>
              <legend>Market data</legend>
              <PillGroup<TradingMarketDataSource> label="Market data source" options={SOURCES} selectedKey={s().marketData} onSelect={src => edit(x => (x.marketData = src))} small />
              <div class={styles.grid2}>
                <label class={styles.field}>
                  Replay directory (on the lab computer)
                  <input value={s().replayDir} onInput={e => edit(x => (x.replayDir = e.currentTarget.value))} data-testid="trading-replay-dir" />
                </label>
                <label class={styles.field}>
                  Watchlist (comma separated)
                  <input value={s().watchlist.join(', ')} onChange={e => edit(x => (x.watchlist = e.currentTarget.value.split(',').map(w => w.trim()).filter(Boolean)))} />
                </label>
                <label class={styles.field}>
                  Oldest usable snapshot (seconds)
                  <input type="number" min="1" value={s().maxSnapshotAgeSeconds} onInput={e => edit(x => (x.maxSnapshotAgeSeconds = num(e.currentTarget.value)))} />
                </label>
              </div>
              <div class={styles.actions}>
                <button
                  type="button"
                  class="outline"
                  disabled={action.busy()}
                  onClick={() => void action.run(async () => {
                    const resp = await workerRpc.installTradingSampleData(props.workerId, {})
                    setDraft(resp.settings)
                    showInfoToast(`Wrote ${resp.files.length} synthetic market files and selected them.`)
                  })}
                  data-testid="trading-install-sample"
                >
                  Use synthetic sample data
                </button>
              </div>
              <p class={styles.hint}>Synthetic data is invented (Black-Scholes prices on made-up symbols SYNA and SYNB). It exercises the workflow; it says nothing about any market.</p>
            </fieldset>

            <fieldset class={styles.fieldset}>
              <legend>Robinhood</legend>
              <p class={styles.hint}>
                Connect the lab to Robinhood so research runs on live quotes and option chains, and live orders can go through the gateway. The Robinhood connection in ChatGPT cannot be shared; signing in here reaches the same Robinhood agent server with LeapMux's own sign-in. Research agents never see it.
              </p>
              <PillGroup<TradingBrokerAuth>
                label="How to connect"
                options={AUTHS}
                selectedKey={authOf(s())}
                onSelect={auth => edit((x) => {
                  if (!x.robinhood)
                    return
                  x.robinhood.auth = auth
                  if (auth === TradingBrokerAuth.SIGN_IN) {
                    x.robinhood.transport = TradingMcpTransport.HTTP
                    x.robinhood.url ||= ROBINHOOD_AGENT_URL
                  }
                })}
                small
              />
              <Show
                when={authOf(s()) === TradingBrokerAuth.SIGN_IN}
                fallback={(
                  <>
                    <PillGroup<TradingMcpTransport> label="Transport" options={TRANSPORTS} selectedKey={s().robinhood?.transport ?? TradingMcpTransport.HTTP} onSelect={tr => edit(x => x.robinhood && (x.robinhood.transport = tr))} small />
                    <div class={styles.grid2}>
                      <Show
                        when={(s().robinhood?.transport ?? TradingMcpTransport.HTTP) === TradingMcpTransport.HTTP}
                        fallback={(
                          <label class={styles.field}>
                            Command
                            <input value={s().robinhood?.command ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.command = e.currentTarget.value))} />
                          </label>
                        )}
                      >
                        <label class={styles.field}>
                          Server URL
                          <input value={s().robinhood?.url ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.url = e.currentTarget.value))} data-testid="trading-mcp-url" />
                        </label>
                      </Show>
                      <label class={styles.field}>
                        Token environment variable
                        <input value={s().robinhood?.tokenEnv ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.tokenEnv = e.currentTarget.value))} placeholder="ROBINHOOD_MCP_TOKEN" />
                      </label>
                      <label class={styles.field}>
                        or token file
                        <input value={s().robinhood?.tokenFile ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.tokenFile = e.currentTarget.value))} />
                      </label>
                      <label class={styles.field}>
                        Account number (empty: the only one)
                        <input value={s().robinhood?.accountId ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.accountId = e.currentTarget.value))} />
                      </label>
                    </div>
                  </>
                )}
              >
                <div class={styles.grid2}>
                  <label class={styles.field}>
                    Server URL
                    <input value={s().robinhood?.url ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.url = e.currentTarget.value))} data-testid="trading-mcp-url" />
                  </label>
                  <label class={styles.field}>
                    Account number (empty: the only one)
                    <input value={s().robinhood?.accountId ?? ''} onInput={e => edit(x => x.robinhood && (x.robinhood.accountId = e.currentTarget.value))} />
                  </label>
                </div>
                <div class={styles.card} data-testid="trading-broker-sign-in">
                  <div class={styles.row}>
                    <StatusBadge label={s().brokerSignIn?.signedIn ? 'Signed in' : 'Not signed in'} tone={s().brokerSignIn?.signedIn ? 'success' : 'warning'} />
                    <Show when={s().brokerSignIn?.signedIn}>
                      <span class={styles.secondary}>{`since ${new Date(Number(s().brokerSignIn?.signedInAtMs ?? 0)).toLocaleString()}`}</span>
                    </Show>
                  </div>
                  <Show when={!s().connectorUrl}>
                    <p class={styles.hint}>Signing in needs this computer's connector address (trading_connector_url), where Robinhood sends you back. Use the lab on the computer that serves the connector.</p>
                  </Show>
                  <Show when={signInUrl()}>
                    {u => (
                      <p class={styles.hint}>
                        <a href={u()} target="_blank" rel="noopener noreferrer">Open the Robinhood sign-in</a>
                        {' (your browser blocked the new tab).'}
                      </p>
                    )}
                  </Show>
                  <div class={styles.actions}>
                    <button type="button" disabled={action.busy() || !s().connectorUrl} onClick={signIn} data-testid="trading-broker-sign-in-button">
                      {s().brokerSignIn?.signedIn ? 'Sign in again' : 'Sign in to Robinhood'}
                    </button>
                    <Show when={s().brokerSignIn?.signedIn}>
                      <button
                        type="button"
                        class="outline"
                        disabled={action.busy()}
                        onClick={() => void action.run(async () => {
                          const resp = await workerRpc.signOutTradingBroker(props.workerId, {})
                          setDraft(resp.settings)
                        })}
                      >
                        Sign out
                      </button>
                    </Show>
                  </div>
                  <p class={styles.hint}>Robinhood opens in a new tab. After you sign in there, come back here, then verify the broker.</p>
                </div>
              </Show>
              <div class={styles.actions}>
                <button
                  type="button"
                  class="outline"
                  disabled={action.busy()}
                  onClick={() => void action.run(async () => {
                    await workerRpc.saveTradingLabSettings(props.workerId, { settings: draft() })
                    const resp = await workerRpc.verifyTradingBroker(props.workerId, {})
                    setBroker(resp.status)
                  })}
                  data-testid="trading-verify-broker"
                >
                  Save and verify broker
                </button>
              </div>
              <Show when={broker()}>
                {b => (
                  <div class={styles.card} data-testid="trading-broker-status">
                    <div class={styles.row}>
                      <StatusBadge label={b().authenticated ? 'Authenticated' : 'Not authenticated'} tone={b().authenticated ? 'success' : 'danger'} />
                      <StatusBadge label={b().liveReady ? 'Ready for live orders' : 'Not ready for live orders'} tone={b().liveReady ? 'success' : 'warning'} />
                      <span class={styles.secondary}>{`checked ${new Date(Number(b().checkedAtMs)).toLocaleString()}`}</span>
                    </div>
                    <div class={t.ticketGrid}>
                      <span>Account</span>
                      <span>{b().accountLabel || '-'}</span>
                      <span>Options level</span>
                      <span>{b().optionsLevel || '-'}</span>
                      <span>Supported</span>
                      <span>{b().supportedOperations.join(', ') || 'none'}</span>
                      <span>Missing</span>
                      <span>{b().missingOperations.join(', ') || 'none'}</span>
                      <span>Research tools</span>
                      <span class={t.mono}>{b().readOnlyTools.join(', ') || 'none'}</span>
                      <span>Gateway-only tools</span>
                      <span class={t.mono}>{b().tradingTools.join(', ') || 'none'}</span>
                      <span>Never used</span>
                      <span class={t.mono}>{b().unclassifiedTools.join(', ') || 'none'}</span>
                    </div>
                    <Show when={b().detail}><p class={styles.hint}>{b().detail}</p></Show>
                  </div>
                )}
              </Show>
            </fieldset>

            <fieldset class={styles.fieldset}>
              <legend>Risk budget (live orders)</legend>
              <div class={styles.grid3}>
                <For each={[
                  ['Max loss per order ($)', 'maxLossPerOrder'],
                  ['Max risk opened per day ($)', 'maxDailyRisk'],
                  ['Max open risk ($)', 'maxOpenRisk'],
                  ['Max contracts per order', 'maxContractsPerOrder'],
                  ['Max open positions', 'maxOpenPositions'],
                  ['Buying power reserve ($)', 'minBuyingPowerReserve'],
                  ['Confirmation window (s)', 'confirmationTtlSeconds'],
                  ['Oldest usable quote (s)', 'maxQuoteAgeSeconds'],
                ] as const}
                >
                  {([label, key]) => (
                    <label class={styles.field}>
                      {label}
                      <input type="number" min="0" value={s().risk?.[key] ?? 0} onInput={e => edit(x => x.risk && ((x.risk[key] as number) = num(e.currentTarget.value)))} />
                    </label>
                  )}
                </For>
              </div>
            </fieldset>

            <fieldset class={styles.fieldset}>
              <legend>Simulation rules (every book)</legend>
              <div class={styles.grid3}>
                <For each={[
                  ['Starting capital ($)', 'startingCapital'],
                  ['Allocation per idea ($)', 'allocationPerIdea'],
                  ['Fee per contract ($)', 'feePerContract'],
                  ['Slippage per contract ($)', 'slippagePerContract'],
                  ['Max spread (fraction of ask)', 'maxSpreadFraction'],
                  ['Min open interest', 'minOpenInterest'],
                  ['Min volume', 'minVolume'],
                ] as const}
                >
                  {([label, key]) => (
                    <label class={styles.field}>
                      {label}
                      <input type="number" min="0" step="any" value={s().simulation?.[key] ?? 0} onInput={e => edit(x => x.simulation && ((x.simulation[key] as number) = num(e.currentTarget.value)))} />
                    </label>
                  )}
                </For>
                <label class={styles.field}>
                  Horizons (days, comma separated)
                  <input value={s().simulation?.horizonDays.join(', ') ?? ''} onChange={e => edit(x => x.simulation && (x.simulation.horizonDays = e.currentTarget.value.split(',').map(d => num(d.trim())).filter(d => d > 0)))} />
                </label>
              </div>
            </fieldset>

            <fieldset class={styles.fieldset} data-testid="trading-connector">
              <legend>Read-only connector (Claude, ChatGPT, Codex)</legend>
              <Show
                when={s().connectorUrl}
                fallback={<p class={styles.hint}>This computer does not serve the connector. A worker started with trading_connector_listen and trading_connector_url lets Claude, ChatGPT or Codex read the lab.</p>}
              >
                {url => (
                  <>
                    <p class={styles.hint}>
                      Add this URL as a custom connector in Claude, ChatGPT or Codex. It can then read runs, predictions, the shadow books and metrics. It cannot prepare, approve or cancel an order.
                    </p>
                    <div class={t.mono} data-testid="trading-connector-url">{url()}</div>
                  </>
                )}
              </Show>
              <p class={styles.hint}>
                {s().hasConnectorPassphrase
                  ? 'A connector passphrase is set. The app asks for it when you connect. Setting a new one signs out every connected app.'
                  : 'No connector passphrase is set, so no app can sign in. Use at least 10 characters.'}
              </p>
              <div class={styles.grid2}>
                <label class={styles.field}>
                  New connector passphrase
                  <input type="password" autocomplete="new-password" value={connectorPass()} onInput={e => setConnectorPass(e.currentTarget.value)} data-testid="trading-connector-passphrase" />
                </label>
              </div>
              <Show when={s().hasConnectorPassphrase}>
                <div class={styles.actions}>
                  <button
                    type="button"
                    class="outline"
                    disabled={action.busy()}
                    onClick={() => void action.run(async () => {
                      const resp = await workerRpc.saveTradingLabSettings(props.workerId, { settings: draft(), signOutConnector: true })
                      setDraft(resp.settings)
                      showInfoToast('Signed out every connected app.')
                    })}
                    data-testid="trading-connector-sign-out"
                  >
                    Sign out connected apps
                  </button>
                </div>
              </Show>
            </fieldset>

            <div class={styles.footer}>
              <button type="button" disabled={action.busy()} onClick={save} data-testid="trading-save-settings">Save settings</button>
            </div>
          </>
        )}
      </Show>
    </div>
  )
}
