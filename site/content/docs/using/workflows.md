---
title: "Workflows"
description: "Define a team of coding agents once, pin each role to a computer, and launch it with an objective: a Lead agent plans and delegates while LeapMux tracks tasks, approvals, retries and recovery across your machines."
type: docs
weight: 13
---

A **workflow** is a reusable team of coding agents. You define the roles once — a Lead, a Builder, an Auditor, an iOS Tester — give each a provider, model and effort, and assign each to one of your computers. Then you launch the workflow with a project and an objective. The **Lead agent** plans the work and delegates it; the other agents start on their assigned computers when the Lead needs them; and LeapMux keeps the run's state — tasks, results, approvals, retries — independently of any agent's conversation.

Define the team → assign each agent to a computer → save the template → enter an objective → launch the Lead → supervise.

## Concepts

| Term | Meaning |
|---|---|
| **Agent profile** | A reusable agent configuration: provider, model and effort, placement, working directory, role instructions, maximum concurrent instances. |
| **Template** | Roles (each from a profile, with optional overrides), the default process and steps, completion criteria, approval gates, retry and concurrency limits. |
| **Run** | One launch of a template: an objective, a project directory, and the template as it was at launch. |
| **Coordinator** | The computer that hosts the run's Lead agent. It owns the run's state. |
| **Task** | A unit of work for one role. The Lead creates tasks, or releases the ones the template planned. |
| **Approval** | A decision only you can make: start a gated task, accept a gated result, allow another attempt, approve a sensitive operation, finish the run. |

## Where a workflow lives

Profiles and templates are stored on the computer you pick as the **library computer** in the Workflows dialog. They never pass through the Hub in plaintext.

A run lives on its **coordinator** — the computer that hosts its Lead. The coordinator keeps the run in its own database and drives it with a background loop, so a run keeps going when you close the browser, and resumes from its saved state when the Hub, the network, or the coordinator itself restarts. The coordinator reaches agents on your other computers through the same end-to-end encrypted channel `leapmux control` uses; the Hub relays only ciphertext.

## Choosing computers

Every role has a **placement**:

| Placement | Behaviour |
|---|---|
| **Specific computer** | The role runs on that computer and nowhere else. If it is offline, the role's tasks wait and say why; LeapMux never moves the work to another machine on its own. |
| **Capability** | Any online computer that has every listed capability, for example `os:darwin` and `xcode`. A role keeps the computer it used last while that computer still qualifies. |
| **Any computer** | Any online computer, optionally limited to an allow-list. |

Each computer reports its capabilities: `os:<name>` and `arch:<name>`, the tools it finds on its `PATH` (`git`, `docker`, `node`, `go`, `python`, and `xcode` on macOS), and anything you add with the `LEAPMUX_WORKER_CAPABILITIES` environment variable (comma-separated) when you start the Worker:

```bash
LEAPMUX_WORKER_CAPABILITIES="gpu,ios-simulator" leapmux worker --hub https://hub.example.com
```

You can override any role's computer when you launch a run, and reassign a task to another computer while it runs. The run records the computer it actually chose for every agent, so recovery reopens nothing elsewhere.

The **Lead runs on the coordinator**: launch a run from the computer the Lead is pinned to, or pick one when the Lead's placement allows several.

## Templates

A template's **steps** are its default process. They appear as planned tasks when the run starts and wait until the Lead releases them with objective-specific instructions (or start at once, for a step marked *auto start*). Steps can depend on each other and carry an **approval gate**: *before start* holds the task until you approve it; *before complete* holds an accepted result until you accept it too.

The Lead is not limited to the steps. It can create any task for any role, at any time, which is how corrections and follow-up checks happen.

Per role, a template also states:

- **Git isolation.** *Worktree* gives each of the role's agents its own git worktree on a run-scoped branch (`wf/<run>/<role>-<agent>`), so two agents never edit the same checkout. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).
- **Required outputs.** What a completed report must carry: `summary`, `commit` (branch and commit SHA), `tests` (that tests ran and passed, with a summary), `artifacts`. LeapMux refuses a report that lacks one — a task is never successful merely because an agent says so.
- **Review.** A reviewed role's result waits for the Lead to accept it or send it back.

Template-wide settings are the default attempt limit, how many tasks may run at once (1 means sequential), the most agents a run may open, whether finishing the run needs your approval, and a list of sensitive operations that every agent is told it must not perform without an approved request.

### Import and export

**Export** saves a template and the profiles it uses as a YAML (or JSON) document; **Import** reads one back. An import always creates new copies, so it never overwrites what your library already holds. Field names follow `proto/leapmux/v1/workflow.proto` (`WorkflowTemplateBundle`):

```yaml
format_version: 1
profiles:
  - id: builder
    name: Builder (Codex, high effort, Windows)
    agent_provider: AGENT_PROVIDER_CODEX
    options: { effort: high }
    placement:
      strategy: WORKER_PLACEMENT_STRATEGY_CAPABILITY
      required_capabilities: [os:windows]
template:
  name: Develop, audit and test on iOS
  roles:
    - { key: lead, name: Lead, profile_id: lead, is_lead: true }
    - key: builder
      name: Builder
      profile_id: builder
      git_isolation: WORKFLOW_GIT_ISOLATION_WORKTREE
      required_outputs: [summary, commit, tests]
      review_required: true
  steps:
    - { key: implement, title: Implement, role_key: builder, instructions: "Implement, test, commit and push." }
```

### The example template

**Add example** installs *Develop, audit and test on iOS*:

- **Lead** (Claude Code, high effort) plans, delegates, reviews the evidence and reports the outcome.
- **Builder** (Codex, high effort, a Windows computer) implements in its own worktree, runs the tests, pushes its branch and reports the commit SHA.
- **Auditor** (Claude Code, high effort) reviews that exact revision independently.
- **iOS Tester** (a Mac with Xcode) builds that exact commit and runs the iPhone Simulator tests, reporting logs and screenshots as artifacts.

When an audit or the tests fail, the Lead delegates a fix to the Builder and sends the failed check back to run again on the new SHA. Each re-run counts as an attempt, and past the limit the task is escalated to you. **Merge** is gated *before start*, so nothing is merged until you approve it.

## Launching a run

Open **Workflows** from the user menu, choose a template and **Launch**:

1. Enter the **objective**, the **project directory**, and optionally a base branch.
2. Review each role's computer and model. A pinned role whose computer is offline is shown with a warning; it will wait rather than move.
3. **Launch**. LeapMux records the run on the Lead's computer, opens the Lead as a tab in the current workspace, and gives it a briefing: the objective, the team and where each member runs, the planned tasks, the rules, and the commands below.

Other agents start when the Lead first delegates to their role, on their assigned computers, and appear as tabs in the same workspace.

## Supervising a run

Opening a run shows the Lead's chat with the **run panel** beside it. The panel shows:

- the run's status, objective and coordinator;
- **approvals** waiting for you, with *Approve* and *Reject*;
- every **agent**: its role, provider and model, the computer it runs on, and its state, with *Open chat* and *Message*;
- every **task**: status and why it waits, attempt count, computer, dependencies, and the structured result — outcome, summary, branch and commit, test results, artifacts and evidence;
- the **audit trail** of delegation, results, reviews, approvals and your own actions.

You can **pause** a run (no new task starts and no message is sent; running agents finish their turn), **resume** it, **cancel** it (every agent's current turn is interrupted), **retry** a blocked or failed task, **reassign** a task to another computer, change a role's model or computer for the agents it opens next, message any agent directly, and open any agent's chat to work with it yourself. Your intervention is recorded in the run; it does not break it.

## How agents take part

A workflow agent drives its run with `leapmux control workflow`. The commands print JSON, and read the run, the coordinator and the agent's credential from the `LEAPMUX_WORKFLOW_*` variables its Worker sets.

| Command | Who | Purpose |
|---|---|---|
| `context` | anyone | The run as this agent may see it: team, placements, tasks (the Lead sees all; others see their own). |
| `delegate --role R --title T --instructions TEXT [--depends-on ID] [--approval before-start\|before-complete] [--max-attempts N]` | Lead | Create a task. `--task ID` releases a planned one instead. |
| `send --task ID --message TEXT` | Lead | A follow-up to the agent working on a task. |
| `review --task ID --accept \| --reject --feedback TEXT` | Lead | Accept a submitted result, or send it back for another attempt on the same agent. |
| `request-approval --title T --details TEXT` | Lead | Ask you to approve a sensitive operation, such as a merge. |
| `complete --summary TEXT [--failed]` | Lead | Finish the run. Refused while a task is open or an approval is pending. |
| `report --task ID --outcome completed\|failed\|blocked --summary TEXT [--branch B --commit SHA] [--tests-passed\|--tests-failed --test-summary TEXT] [--artifact PATH] [--evidence label=value]` | assignee | Report a task's structured result. |
| `progress --task ID --note TEXT` | assignee | A progress note shown on the task. |

Any text value may be `-` to read it from standard input. The engine tells the Lead about every result, failure, escalation and decision with a message in its chat, so the Lead does not need to poll.

## Your agents, your rules

Workflow agents are ordinary LeapMux agents. Each keeps the whole `leapmux control` surface, so any of them can open **helper agents** (`leapmux control tab open --type agent ...`) whenever that gets the work done faster, read other tabs, or drive a workflow itself — exactly as you can. The Lead's briefing says so. The run tracks the tasks delegated through it; helpers an agent opens on its own are ordinary tabs.

What the workflow adds is structure, and every piece of it is a template setting you choose:

- **Approval gates** on steps, **final approval** of the run, and the list of **sensitive operations** agents are told to ask about. None are on unless the template sets them (the example gates only its Merge step).
- **Required outputs** per role. A role with none accepts any report.
- **Limits**: how many agents a run may open for its roles, how many tasks run at once, how many attempts a task gets before you are asked.
- **Restrict agents to this workflow** (off by default). When on, each agent's control socket reaches only its run's commands on the coordinating computer: no helper agents, no other tabs, no other computers. Use it when you want a run fenced in.

Two things hold regardless: a role pinned to a computer waits for that computer rather than moving on its own, and every delegation, result, review, approval and intervention is recorded in the run's audit trail. Each agent also carries its own credential for the run, which is how the engine knows who is speaking: only the Lead delegates, reviews or finishes the run, and only a task's assignee reports it.

## Recovery

Every change to a run is saved before it acts, and every action is safe to repeat:

- A message to an agent is recorded first and sent with a stable id; the receiving computer delivers it once, however often a reconnect resends it.
- An agent being started is recorded first; after a restart, the coordinator asks that computer which agents it already opened for the run and adopts them instead of starting a second one.
- Every 30 seconds, and when you press *Refresh*, the coordinator checks each agent and computer. An unreachable computer marks its agents unreachable without re-running their work. An agent that is gone has its task queued again on a fresh agent (on the same pinned computer) while attempts remain, and is escalated to you after that. A Lead that is gone blocks the run.

Re-running a task is a new attempt with the task's history and feedback; completed tasks are never re-dispatched.

## Limitations

- A run's Lead must stay on its coordinator; it cannot be moved to another computer after launch.
- If the Lead's agent is closed, the run is blocked and can only be cancelled.
- A reported commit SHA is checked for its form, not fetched from the reporting computer's repository.
- Models and efforts are entered per provider: LeapMux has no model catalog until an agent is running, so a value the provider does not accept fails when that agent starts.
- The run panel refreshes by polling the coordinator every few seconds while it is open.
