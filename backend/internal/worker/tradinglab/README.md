# Blind trading research lab

`blind-trading-lab` is a workflow template plus the privileged components it
needs. It runs independent research agents that each propose short-dated,
high-risk options trades from the same immutable market snapshot. It then
freezes and compares their recommendations, books three shadow portfolios, and
can submit a real order through a Robinhood MCP server only after a human
approves that exact order.

It is built on the reusable workflow engine (`internal/worker/workflow`), not
beside it. Anything a different experiment could use lives in the engine.
Only the market, portfolio and broker logic lives here.

## What already existed

| Piece | Where | Reused for |
|---|---|---|
| Durable run state machine (runs, tasks, slots, approvals, events, outbox) on the coordinator's SQLite | `workflow/engine.go`, `runs.go`, `store.go` | the lab's run lifecycle, recovery after a process or worker restart, the audit trail |
| Template, profile and role resolution, YAML/JSON bundles (`format_version: 1`) | `workflow/spec.go`, `library.go` | the versioned template schema, provider/model/effort/permission and worker placement per role |
| Worker placement (specific computer, capability, any) | `workflow/placement.go` | assigning each analyst to a computer |
| Per-slot credential, at-most-once delivery, reconcile of lost agents | `engine.go` | fresh analyst sessions that survive restarts |
| Control-socket confinement (`confine_agents`) | `controlipc/router.go`, `factory.go` | no cross-agent control permissions |
| `leapmux control workflow ...` CLI | `cli/control/cmd/workflow.go` | analysts report structured predictions |
| Workflows dialog, launch form, run panel | `frontend/src/components/workflows/` | template configuration, agent/model/worker assignment, run controls |

## What the engine gains (generic)

1. **Template format version 2** with a JSON Schema
   (`workflow/schema/workflow-template.v2.schema.json`) that an import validates
   before it reads the document. Version 1 documents still import.
2. **Engine coordination** (`coordination: WORKFLOW_COORDINATION_ENGINE`). A run
   has no LLM Lead: every step is released at launch and starts when its
   dependencies succeed. The run finishes by itself when every task is final.
   A Lead sees every result, so it could pass one analyst's hypothesis to another.
   An engine-coordinated run has no participant that sees everything.
3. **Structured step outputs.** A step may declare an `output_schema` (JSON
   Schema). A completed report must then carry `structured_output`, which the
   engine validates before it accepts the report.
4. **Frozen outputs.** A step with `freeze_output` stores its accepted output
   as canonical JSON with its SHA-256 in `workflow_frozen_outputs`. Triggers
   refuse every UPDATE and DELETE on that table, and a run that holds frozen
   outputs cannot be deleted.
5. **Declared step inputs.** `inputs: [{step_key, label, anonymize, redact_fields}]`
   is the only way one step's output reaches another agent. The engine
   attaches the frozen outputs of those steps to the dispatch message. With
   `anonymize`, the engine relabels the outputs as "Submission A", "Submission B"
   and so on, in an order seeded by the run and step, and records the mapping
   so the UI can reveal it after scoring.
6. **Executor steps.** A step with `executor: <name>` runs in-process on the
   coordinator through a registered `workflow.Executor` instead of an agent.
   Executors are idempotent per task attempt and are re-run after a restart.
   The lab registers `tradinglab.snapshot` and `tradinglab.record`.
7. **Role isolation.** `isolation: {fresh_agent_per_task, private_working_dir,
   confine, blind}`:
   - `fresh_agent_per_task` never reuses a session.
   - `private_working_dir` makes the hosting worker create an empty 0700
     directory per slot under its data directory and start the agent there.
   - `confine` confines the socket per role.
   - `blind` drops the run's objective, name, team and other roles from the
     briefing, so a blind analyst learns only its own role, its task and its
     declared inputs.

## What this package adds

```
tradinglab/
  prediction.go      prediction, evaluation and audit schemas (embedded JSON Schema) + semantic checks against the snapshot
  snapshot.go        MarketSnapshot, freshness checks, compaction, canonical hash
  marketdata.go      MarketData interface; Robinhood MCP (read-only) and replay providers
  robinhood.go       Robinhood MCP adapter: tool discovery, auth/capability verification, read and trade surfaces kept apart
  catalysts.go       news and corporate-event collector (MCP tools when present, replay file otherwise)
  shadow.go          three shadow books, bid/ask fill simulator (spread, slippage, fees, expiry, missing data)
  metrics.go         horizon and expiration P/L, risk-normalized return, win rate, drawdown, Brier calibration, 3x/5x frequency
  gateway.go         privileged execution gateway: prepare, preview, exact-order approval, idempotent submit, reconcile
  policy.go          precisely scoped execution policies (the only path to an order without a fresh confirmation)
  store.go           lab tables (migration 00003); live fills kept apart from simulated fills
  executors.go       the workflow executors
  service.go         the lab's inner RPCs (proto/leapmux/v1/trading_lab.proto)
internal/util/mcpclient/  a minimal MCP client (stdio and streamable HTTP)
```

### Security boundaries

- Research agents never receive broker credentials, the MCP endpoint or any
  order tool. Market data reaches them only as frozen snapshot JSON in their
  task message. Their sockets are confined to the participant methods of their
  own run.
- The Robinhood adapter exposes two Go types. `ReadOnlyBroker` holds the
  read tools, and the snapshot executor gets only that type. `TradingBroker`
  holds preview, place and cancel, and only the gateway gets that type.
  Verification refuses to treat a tool as read-only unless it is on the
  read-only allowlist.
- Credentials come from the worker's environment or a secrets file named in
  the lab settings, never from a template, a run or the database.
- **No live order happens without a human.** The gateway submits only:
  - an order whose exact canonical parameters (account, symbol, contract,
    side, quantity, limit price, time in force) hash to the fingerprint a
    person approved in their own browser, within its time limit; or
  - an order matching an active execution policy that a person approved,
    with a scope, quantity and price limit and an expiry.

  Closing orders follow the same rule. AI output can add a candidate to the
  queue. It never approves one.
- "A person" is a channel the Hub opened for a browser session
  (`ChannelOpenRequest.browser_session`), reaching the worker through no
  local control socket. An agent, an API token, an app and the read-only
  connector all act as the owner, but none is a browser session, so none can
  approve an order or a policy, switch to live mode or sign in to the broker.
- The broker sign-in (`brokerauth.go`) is OAuth 2.1 with PKCE against the
  broker's MCP server. The worker keeps the tokens sealed for its OS user
  (DPAPI on Windows) and refreshes them; only the lab's adapter uses them.
- Paper mode is the default and needs no credentials. In paper mode the
  gateway refuses every live submission.
- The read-only Claude connector (`internal/worker/labmcp`), served only when
  the worker sets `trading_connector_listen`, calls the lab's read methods
  and nothing else. It signs a person in with a connector passphrase of its
  own. It never returns the broker settings, the account number or an
  order's fingerprint.

### State and recovery

The engine and the lab keep every state in the coordinator's database, and
every external call sits between a recorded intent and a recorded outcome.

- A crashed snapshot executor re-runs. It reuses the snapshot already stored
  for that task.
- An order intent moves through these states:
  `PREPARED -> CONFIRMED -> SUBMITTING -> SUBMITTED -> FILLED/PARTIAL/CANCELLED/REJECTED`.
  The intent row and a deterministic client order id are written before the
  broker call. After a crash in `SUBMITTING`, reconciliation looks the order up
  at the broker before anything is resubmitted.

## Known limits

- **The Robinhood MCP tool vocabulary is not verified against a live
  server.** The adapter discovers tools by name (`defaultToolNames`,
  overridable per operation in the settings) and reads results tolerantly
  (common field spellings, `results`/`data` envelopes). Before trusting live
  data, run **Verify broker** against your server and check the reported
  operations; the tests use a fake server with the shapes documented in
  `robinhood.go`. The broker sign-in follows the server's published OAuth
  metadata; it was checked against Robinhood's metadata and registration,
  not yet against a completed sign-in.
- **Human-only actions rest on the browser session.** The Hub tells the
  worker whether a person's cookie session opened the channel; a call an
  agent relays through another worker arrives on a delegation and is refused,
  as is one through a local control socket. An agent that could drive the
  person's own logged-in browser could still click an approval: keep agents
  away from that browser profile.
- **Agents share the OS user.** Private directories and confined sockets keep
  LeapMux surfaces apart, but a provider with unrestricted shell access can
  read the user's files. Use the provider's sandbox or another computer for
  stronger isolation.
- **Long calls and puts only.** Defined-risk spreads need multi-leg orders,
  previews and fills, and a spread-aware simulator.
- **Executors run on the coordinator.** The snapshot reaches analysts on
  other computers inline in their task message (bounded at 1 MiB of inputs).

## Implementation plan (each stage ships with tests)

1. Generic engine: proto fields, migration `00003`, engine coordination,
   schemas, freezing, inputs, executors, isolation, template v2 JSON Schema.
2. Lab domain: prediction/evaluation/audit schemas and checks, snapshots and
   freshness, MCP client, Robinhood adapter with verification, catalyst
   collector, replay provider.
3. Shadow portfolios, fill simulator and metrics.
4. Execution gateway, policies and reconciliation, against a fake Robinhood
   MCP server.
5. Executors, inner RPCs, service wiring, `leapmux control lab` CLI and the
   bundled `blind-trading-lab` template.
6. Frontend Trading Research panel.
7. End-to-end test of a paper-only run.
