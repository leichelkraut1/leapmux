import type { OpenAgentTarget } from './WorkflowRunPanel'
import type { AgentInfo, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import type { WorkflowComputer } from '~/stores/workflows'
import { useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, on, onCleanup, onMount, Show, untrack } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { hasPlaceableTab, openTabInFocusedTile } from '~/components/shell/openTabInFocusedTile'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { makeIdGenerator } from '~/lib/idGenerator'
import { tradingResearchTabPayload } from '~/lib/tabPayload'
import { openedAgentTabFields } from '~/stores/tab.helpers'
import { emitRemoveTab } from '~/stores/tabOps'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { defaultComputerId, formatRunParam, parseRunParam } from '~/stores/workflows'
import { tradingTab } from './trading/tradingTab'
import { WorkflowRunPanel } from './WorkflowRunPanel'
import { WorkflowsDialog, workflowsTabFromParam } from './WorkflowsDialog'

export interface WorkflowsHostProps {
  workers: readonly Worker[]
  workerInfo: (workerId: string) => WorkerInfo | null
  activeWorkspaceId: () => string | null | undefined
  isWorkspaceMutatable: (workspaceId: string) => boolean
  view: TabView
  layoutStore: ReturnType<typeof createLayoutStore>
  selection: TabSelectionStore
  metadata: TabMetadataStore
  repoGitStore: ReturnType<typeof createRepoGitStore>
  selectTab: (tab: Tab) => void
  selectWorkspace: (workspaceId: string) => void
  availableProviders?: AgentProvider[]
}

/**
 * The Workflows surfaces and their wiring into the shell:
 *
 *   - `?workflows=runs|templates|profiles` opens the dialog;
 *   - `?workflow=<coordinatorWorkerId>:<runId>` docks the run panel beside
 *     the chats, non-modal, so the Lead's conversation stays in view;
 *   - `?trading=open` (or `=<workerId>`) opens a Trading research tab, or
 *     focuses the one the lab computer already has.
 *
 * Tab placement goes through `openTabInFocusedTile`, exactly as the New Agent
 * dialog's success path does, so a workflow agent's tab is an ordinary tab.
 */
export function WorkflowsHost(props: WorkflowsHostProps) {
  const [searchParams, setSearchParams] = useSearchParams()

  const computers = createMemo<WorkflowComputer[]>(() => props.workers.map((worker) => {
    const info = props.workerInfo(worker.id)
    return {
      id: worker.id,
      name: info?.name || worker.id,
      online: worker.online,
      os: info?.os ?? '',
      ...(info?.capabilities !== undefined ? { capabilities: info.capabilities } : {}),
    }
  }))

  // A Trading research tab renders deep in the tile tree; it reads the
  // computers and opens other labs' tabs through this.
  createEffect(() => tradingTab.setComputers(computers()))

  const generateTradingTabId = makeIdGenerator('trading')
  /** Focus the lab computer's Trading research tab, or open one in the active workspace. */
  const openTradingTab = (labWorkerId?: string) => {
    const workerId = labWorkerId || defaultComputerId(untrack(computers))
    if (!workerId) {
      showWarnToast('Cannot open Trading research', new Error('No computer is connected to hold the lab.'))
      return
    }
    // With no computer named, any lab's open tab is the one to return to.
    const existing = props.view.all().find(t => t.type === TabType.TRADING_RESEARCH && (!labWorkerId || t.workerId === workerId))
    if (existing) {
      props.selectWorkspace(existing.workspaceId)
      props.selectTab(existing)
      return
    }
    const workspaceId = props.activeWorkspaceId()
    if (!workspaceId || !props.isWorkspaceMutatable(workspaceId)) {
      showWarnToast('Cannot open Trading research', new Error('Open a workspace that is not archived first: the tab goes into the active workspace.'))
      return
    }
    const tabId = generateTradingTabId()
    const placed = openTabInFocusedTile(props, { type: TabType.TRADING_RESEARCH, id: tabId, workerId }, { hydrated: true })
    // A refused placement added no tab, so no worker row may be registered for it.
    if (!placed) {
      showWarnToast('Cannot open Trading research', new Error('The workspace is not ready for a new tab yet.'))
      return
    }
    workerRpc.registerTabPayload(workerId, { tabId, payload: tradingResearchTabPayload() }).catch((err) => {
      emitRemoveTab(TabType.TRADING_RESEARCH, tabId)
      showWarnToast('Cannot open Trading research', err)
    })
  }
  onMount(() => {
    tradingTab.setOpener(openTradingTab)
    onCleanup(() => tradingTab.setOpener(undefined))
  })

  // `?trading=` opens the tab once and clears itself; the old dialog section
  // (`?workflows=trading`) lands here too.
  createEffect(on(() => [searchParams.trading, searchParams.workflows] as const, ([trading, workflows]) => {
    const raw = Array.isArray(trading) ? trading[0] : trading
    const legacy = (Array.isArray(workflows) ? workflows[0] : workflows) === 'trading'
    if (!raw && !legacy)
      return
    setSearchParams({ trading: undefined, ...(legacy ? { workflows: undefined } : {}) }, { replace: true })
    openTradingTab(raw && raw !== 'open' ? raw : undefined)
  }))

  const dialogOpen = () => searchParams.workflows !== undefined && searchParams.workflows !== ''
  const runRef = createMemo(() => parseRunParam(searchParams.workflow), undefined, {
    equals: (a, b) => a?.coordinatorWorkerId === b?.coordinatorWorkerId && a?.runId === b?.runId,
  })

  // Capabilities arrive with each computer's system info; ask the online ones
  // when a workflow surface opens (the store dedupes and honours its TTL).
  createEffect(on(() => dialogOpen() || runRef() !== undefined, (open) => {
    if (!open)
      return
    for (const worker of props.workers) {
      if (worker.online)
        void workerInfoStore.fetchWorkerInfo(worker.id)
    }
  }))

  const launchBlockedReason = (): string | undefined => {
    const workspaceId = props.activeWorkspaceId()
    if (!workspaceId)
      return 'Open a workspace first: the agents\' tabs go into the active workspace.'
    if (!props.isWorkspaceMutatable(workspaceId))
      return 'This workspace is archived. Unarchive it to launch a workflow into it.'
    if (!hasPlaceableTab(props.layoutStore))
      return 'The workspace view is not ready yet. Try again in a moment.'
    return undefined
  }

  const placeLeadTab = (agent: AgentInfo): boolean => {
    const placed = openTabInFocusedTile(
      props,
      { type: TabType.AGENT, id: agent.id, workerId: agent.workerId },
      openedAgentTabFields(props.repoGitStore, agent),
    )
    if (!placed)
      showWarnToast('Cannot open the Lead\'s tab', new Error('The workspace is not ready for a new tab yet. The run continues; open the Lead from the run panel.'))
    return placed !== ''
  }

  /** Focus an agent's tab; with `open`, place one in the run's workspace when it has none. */
  const focusAgent = (target: OpenAgentTarget, open: boolean) => {
    const tab = props.view.getAgentTab(target.agentId)
    if (tab) {
      props.selectTab(tab)
      props.selectWorkspace(tab.workspaceId)
      return
    }
    if (!open)
      return
    if (!target.workspaceId || !props.isWorkspaceMutatable(target.workspaceId)) {
      showWarnToast('Cannot open the agent', new Error('The run\'s workspace is not available.'))
      return
    }
    // Select first: placement resolves against the ACTIVE workspace's tile.
    props.selectWorkspace(target.workspaceId)
    const placed = openTabInFocusedTile(props, { type: TabType.AGENT, id: target.agentId, workerId: target.workerId }, {})
    if (!placed)
      showWarnToast('Cannot open the agent', new Error('The workspace is not ready for a new tab yet.'))
  }

  return (
    <>
      <Show when={dialogOpen()}>
        <WorkflowsDialog
          tab={workflowsTabFromParam(searchParams.workflows)}
          onTabChange={tab => setSearchParams({ workflows: tab }, { replace: true })}
          computers={computers()}
          workspaceId={props.activeWorkspaceId() ?? undefined}
          {...(() => {
            const reason = launchBlockedReason()
            return reason !== undefined ? { launchBlockedReason: reason } : {}
          })()}
          {...(props.availableProviders !== undefined ? { availableProviders: props.availableProviders } : {})}
          homeDirOf={workerId => props.workerInfo(workerId)?.homeDir ?? ''}
          placeLeadTab={placeLeadTab}
          onLaunched={launched => setSearchParams({
            workflows: undefined,
            workflow: formatRunParam({ coordinatorWorkerId: launched.coordinatorWorkerId, runId: launched.runId }),
          })}
          onOpenRun={row => setSearchParams({ workflows: undefined, workflow: row.key })}
          onClose={() => setSearchParams({ workflows: undefined }, { replace: true })}
        />
      </Show>
      <Show when={runRef()} keyed>
        {ref => (
          <WorkflowRunPanel
            runRef={ref}
            computers={computers()}
            onOpenAgent={target => focusAgent(target, true)}
            onLeadKnown={target => focusAgent(target, false)}
            onDeleted={() => setSearchParams({ workflow: undefined }, { replace: true })}
            onClose={() => setSearchParams({ workflow: undefined }, { replace: true })}
          />
        )}
      </Show>
    </>
  )
}
