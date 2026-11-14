---
title: "Trading research"
description: "Run isolated options analysts on one frozen market snapshot, compare their hashed predictions anonymously, track three shadow portfolios, and place a real order through Robinhood MCP only when you confirm that exact order."
type: docs
weight: 14
---

**Blind trading research** is a bundled [workflow](../workflows/) template plus a privileged lab on its coordinating computer. Two research agents independently look for short-dated, high-risk long call and long put ideas in the same frozen market snapshot. One is a neutral analyst; the other looks for mispriced expectations. Their predictions are frozen with a SHA-256 hash. An evaluator scores them, and a risk auditor checks them; both see the submissions only as "Submission A" and "Submission B". The lab then records every candidate and books three shadow portfolios.

Nothing in this process trades. A real order happens only when you confirm that exact order, with your passphrase, in the Trading Research view.

## How a run works

| Step | Who | What it produces |
|---|---|---|
| Freeze market snapshot | the lab, on the coordinator | Quotes, option chains with bid/ask, IV, Greeks, open interest and volume, catalysts, provenance, freshness. Frozen with its hash. |
| Blind research, Contrarian research (at the same time) | two agents | A prediction document per analyst: exact contracts, entry, maximum loss, catalyst, scenarios, probabilities, exits, invalidation. Frozen with its hash. |
| Anonymous evaluation, Risk audit (at the same time) | two agents | Rubric scores, and audit findings for every recommendation. Frozen. |
| Record candidates and shadow books | the lab | Every candidate (eligible, rejected or no trade) and the three shadow books. |

The run is **coordinated by the engine**: there is no Lead agent, because a Lead would see everything and could pass one analyst's hypothesis to the other. Each step starts when its inputs are frozen.

### Isolation

Every research role runs in a **fresh session**, in a **private, empty directory** on its computer, with its control socket **confined** to its own run. Each role is also **blind**: its briefing and `workflow context` omit the run's name and objective, the template, and the other roles. An analyst receives only its instructions and the frozen snapshot. The evaluator and the auditor receive only frozen, anonymized submissions.

All agents run as your operating system user, so a determined agent with shell access could still read files you can read. To close that gap, run each analyst under its provider's sandbox or on a separate computer, which role placement supports.

### What the lab checks itself

The lab does not take an agent's word. It decides eligibility itself:

- The prediction cites the frozen snapshot's hash.
- The contract exists in the snapshot and matches its strike, expiry and right.
- The entry limit is executable at the recorded ask plus slippage.
- The spread, open interest and volume pass your limits.
- The stated maximum loss equals the limit × 100 plus fees.
- The probabilities and exits are consistent.

The evaluation total is computed by the lab from the evaluator's component scores with one fixed rubric, so identical scores always total the same, whichever submission they belong to.

## Shadow portfolios

| Book | Holds |
|---|---|
| Blind analyst | every recommendation of the blind analyst |
| Contrarian analyst | every recommendation of the contrarian analyst |
| Blind, opposite direction | each blind idea implemented in the opposite direction: the other right at the nearest delta, priced from the snapshot on its own |
| Live | real fills only, reconciled from the broker |

All three simulated books follow the same rules:

- Fill at the **ask plus slippage**, never the midpoint, and sell at the bid less slippage.
- Pay the same fee per contract.
- Size every idea by the same allocation.
- Record an idea the quote cannot fill as **not executable**.
- Settle at expiry.

**Mark books** reads later market data and records each position's value at your fixed horizons and at expiry. For each book, the dashboard shows net P/L, return on risk, risk-adjusted return, win rate, maximum drawdown, Brier calibration, and how often the bid reached 3× and 5× the entry.

## Paper mode (the default)

A new lab is paper-only and needs no credentials.

1. Open **Trading research** from the user menu. It opens as a tab in the active workspace, showing the lab of one computer; picking another computer in it opens that computer's tab. Then **Settings → Use synthetic sample data** writes an invented market recording and selects it. The symbols SYNA and SYNB are made up, and the prices come from Black-Scholes.
2. **Templates → Add example: Blind trading research**, then **Launch**. Choose the coordinating computer and the computers for the four roles.
3. Follow the run under **Trading research → Research runs**, then **Mark books** under **Portfolios**.

For real market data without trading, set the market data source to **Robinhood MCP (read-only)**. Research then uses only the server's read tools.

## Connecting Robinhood

The Robinhood connection inside ChatGPT belongs to ChatGPT and cannot be shared. LeapMux signs in to the same server, Robinhood's MCP server for agents (`https://agent.robinhood.com/mcp/trading`), with a sign-in of its own:

1. In **Trading research → Settings → Robinhood**, choose **Sign in**. The server URL fills in.
2. Choose **Sign in to Robinhood**. Robinhood's sign-in page opens in a new tab; sign in there and approve LeapMux. Robinhood sends you back to the computer's connector address, so the lab must run on a computer that serves the connector (see [Reading the lab from Claude or ChatGPT](#reading-the-lab-from-claude-or-chatgpt)).
3. Back in LeapMux, the settings show **Signed in**. Choose **Save and verify broker**.

The worker keeps the sign-in's tokens, sealed for its own Windows user (DPAPI) on Windows, and refreshes them. No research agent ever sees them. **Sign out** forgets them.

Robinhood's agent server trades in the account you connected to it, which Robinhood may keep separate from your main account.

A server that takes a fixed token instead can still be configured under **Token on this computer**: its URL or start command, and the environment variable or file that holds the token.

**Save and verify broker** connects to the server and reports:

- whether an account read succeeded;
- which operations exist;
- which tools are research-only and which are gateway-only;
- which tools are never used.

A read operation that maps to a tool that looks like it places or changes orders is refused. If the server's tool names differ from the defaults, map them in the settings.

## Placing a real order

1. Switch to **Live** mode. This needs a broker verified as ready.
2. In a research run, choose **Add to approved queue** on an eligible idea. Approval places nothing.
3. Under **Queue and orders**, choose **Prepare order**. The gateway runs these checks, then shows the exact order, its maximum loss, fees and the broker's alerts:
   - It re-reads your account's options permissions and buying power.
   - It refreshes the quote and checks that the contract is tradable and unexpired.
   - It applies your risk budget.
   - It runs the broker's preview.
4. Choose **Place this order…**, check the order in the prompt, and choose **Approve and place** before the confirmation window closes. The gateway submits that order once, with an idempotency key.
5. **Reconcile with broker** records fills in the Live book. If a submission's answer was lost, reconciliation finds the order by its key and never resends it.

Closing a live position works the same way. The other route is an **execution policy**, which you approve the same way. It may only close one live position: up to a stated quantity, at or above a stated price, until it expires. It is used once.

The worker accepts an approval only from your own browser session. An agent, an API token, an app or the read-only connector holds your identity too, but none of them is a browser session, so none can approve an order or a policy, switch to live mode or sign in to the broker. A research agent cannot reach the lab at all.

## Reading the lab from Claude or ChatGPT

A Worker can serve a read-only connector, so Claude, ChatGPT or Codex (on your phone, on the web or on a computer) can answer questions about the lab: how each book is doing, what a run predicted, which ideas are queued. It reads the lab of the Worker that serves it, so serve it from the computer your research runs use.

The connector has five tools: an overview of the lab and its books, the research runs, one run's snapshot, predictions, verdicts and candidates, the positions, and the orders. None of them prepares, confirms or cancels an order, decides a candidate or reaches the broker. They never return a broker credential, the account number or an order's confirmation fingerprint.

To set it up:

1. Start the Worker with `trading_connector_listen` (a local address) and `trading_connector_url` (the public `https` URL). See [Worker configuration](/docs/admin/configuration/).
2. Publish the local address at that URL with a reverse proxy, including the two `/.well-known/` paths the configuration describes.
3. In **Trading research > Settings**, set a connector passphrase of at least 10 characters. It only signs apps in to read the lab; it never approves an order.
4. Add the URL as a custom connector: in Claude under **Settings > Connectors**, in ChatGPT under **Settings > Apps & Connectors** (developer mode), or as an MCP server in Codex. The app opens a sign-in page that asks for the connector passphrase.

Sign-in uses OAuth 2.1 with PKCE, and codes go only to Claude's or ChatGPT's own callback or to a loopback address. Five wrong passphrases in 15 minutes lock sign-in for 15 minutes. An access token lasts an hour and a refresh token 30 days. Setting a new connector passphrase, or **Sign out connected apps**, ends every session.
