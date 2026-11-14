import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { createLayoutStore } from '~/stores/layout.store'
import type { Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import type { WorkflowComputer } from '~/stores/workflows'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { openTabInFocusedTile } from '~/components/shell/openTabInFocusedTile'
import { TAB_TYPE_WIRE_TOKEN } from '~/generated/contracts/tab-types'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { makeIdGenerator } from '~/lib/idGenerator'
import { tradingResearchTabPayload } from '~/lib/tabPayload'
import { workerInfoStore } from '~/stores/workerInfo.store'

export interface OpenTradingTabDeps {
  workers: readonly Worker[]
  workerInfo: (workerId: string) => WorkerInfo | null
  computers: readonly WorkflowComputer[]
  activeWorkspaceId: () => string | null | undefined
  isWorkspaceMutatable: (workspaceId: string) => boolean
  view: TabView
  layoutStore: ReturnType<typeof createLayoutStore>
  selection: TabSelectionStore
  metadata: TabMetadataStore
  selectTab: (tab: Tab) => void
  selectWorkspace: (workspaceId: string) => void
}

const generateTradingTabId = makeIdGenerator('trading')

function cannotOpen(message: string): void {
  showWarnToast('Cannot open Trading research', new Error(message))
}

/**
 * Whether a computer's worker can hold a Trading research tab: its system
 * info lists "tab:<wire token>". An older worker refuses the tab, and its tab
 * report would drop it, so the tab would vanish the moment it opened.
 */
async function holdsTradingTab(deps: OpenTradingTabDeps, workerId: string): Promise<boolean> {
  const info = deps.workerInfo(workerId) ?? await workerInfoStore.fetchWorkerInfo(workerId)
  return (info?.capabilities ?? []).includes(`tab:${TAB_TYPE_WIRE_TOKEN[TabType.TRADING_RESEARCH]}`)
}

/**
 * The lab computer to open when none is named: an online computer that can
 * hold the tab, preferring the one serving the trading connector, which is
 * where the lab people use lives.
 */
async function chooseLabComputer(deps: OpenTradingTabDeps): Promise<string | undefined> {
  const able: string[] = []
  for (const worker of deps.workers.filter(w => w.online)) {
    if (await holdsTradingTab(deps, worker.id))
      able.push(worker.id)
  }
  if (able.length <= 1)
    return able[0]
  for (const id of able) {
    const resp = await workerRpc.getTradingLabSettings(id, {}).catch(() => undefined)
    if (resp?.settings?.connectorUrl)
      return id
  }
  return able[0]
}

/**
 * Focus the lab computer's Trading research tab, or open one in the active
 * workspace. The worker accepts the tab before it is placed, so a computer
 * that cannot hold it gets a message rather than a tab that opens and
 * vanishes.
 */
export async function openTradingResearchTab(deps: OpenTradingTabDeps, labWorkerId?: string): Promise<void> {
  // With no computer named, any lab's open tab is the one to return to.
  const existing = deps.view.all().find(t => t.type === TabType.TRADING_RESEARCH && (!labWorkerId || t.workerId === labWorkerId))
  if (existing) {
    deps.selectWorkspace(existing.workspaceId)
    deps.selectTab(existing)
    return
  }
  const workspaceId = deps.activeWorkspaceId()
  if (!workspaceId || !deps.isWorkspaceMutatable(workspaceId))
    return cannotOpen('Open a workspace that is not archived first: the tab goes into the active workspace.')
  const workerId = labWorkerId ?? await chooseLabComputer(deps)
  if (!workerId)
    return cannotOpen('No connected computer runs a LeapMux worker that has the Trading research tab. Update the lab computer\'s worker, and check it is online.')
  const name = deps.computers.find(c => c.id === workerId)?.name ?? workerId
  if (!deps.workers.find(w => w.id === workerId)?.online)
    return cannotOpen(`${name} is offline.`)
  if (labWorkerId && !await holdsTradingTab(deps, workerId))
    return cannotOpen(`${name} runs an older LeapMux worker without the Trading research tab. Update its worker first.`)
  const tabId = generateTradingTabId()
  try {
    await workerRpc.registerTabPayload(workerId, { tabId, payload: tradingResearchTabPayload() })
  }
  catch (err) {
    showWarnToast(`Cannot open Trading research on ${name}`, err)
    return
  }
  const placed = openTabInFocusedTile(deps, { type: TabType.TRADING_RESEARCH, id: tabId, workerId }, { hydrated: true })
  if (!placed) {
    void workerRpc.revokeTabPayload(workerId, { tabId }).catch(() => {})
    cannotOpen('The workspace is not ready for a new tab yet.')
  }
}
