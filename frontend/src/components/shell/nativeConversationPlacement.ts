import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import type { NativeConversationDestination } from '~/components/dashboard/AttachNativeConversationDialog'
import type { RepoGitStore } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { basename, detectFlavor, normalizeSeparators } from '~/lib/paths'
import { repoGitView } from '~/stores/repoGit'

export interface NativeConversationPlacement {
  workspaceId: string
  workerId: string
  workingDir: string
}

// Git reports Windows checkouts with '/', while the worker stores native '\'.
// This is comparison only: neither the stored placement nor source cwd changes.
function workingDirKey(path: string): string {
  const flavor = detectFlavor(path)
  const normalized = normalizeSeparators(path, flavor)
  return flavor === 'win32' ? normalized.toLowerCase() : normalized
}

/** Destination identity is independent of the source conversation's PC and cwd. */
export function nativeConversationPlacementKey(at: NativeConversationPlacement): string {
  return JSON.stringify([at.workspaceId, at.workerId, workingDirKey(at.workingDir)])
}

export function nativeConversationMatches(tab: Tab, ref: NativeConversationRef, at: NativeConversationPlacement): boolean {
  return tab.type === TabType.NATIVE_CONVERSATION
    && tab.workspaceId === at.workspaceId && tab.workerId === at.workerId && workingDirKey(tab.workingDir ?? '') === workingDirKey(at.workingDir)
    && tab.sourceWorkerId === ref.workerId && tab.agentProvider === ref.agentProvider && tab.sessionId === ref.sessionId
}

/** Only an existing physical checkout can anchor a new sidebar link. */
export function nativeConversationPlacementAnchor(tabs: readonly Tab[], at: NativeConversationPlacement, git: RepoGitStore): Tab | undefined {
  return tabs.find(tab => tab.type !== TabType.NATIVE_CONVERSATION && !!tab.tileId
    && tab.workspaceId === at.workspaceId && tab.workerId === at.workerId
    && repoGitView(tab, git).isGitRepo && workingDirKey(repoGitView(tab, git).toplevel ?? '') === workingDirKey(at.workingDir))
}

export function nativeConversationDestinations(opts: {
  tabs: readonly Tab[]
  workspaces: readonly { id: string, name: string }[]
  git: RepoGitStore
  isWorkspaceMutatable: (id: string) => boolean
  workerOnline: (id: string) => boolean
  workerName: (id: string) => string | undefined
}): NativeConversationDestination[] {
  const workspaces = new Map(opts.workspaces.filter(w => opts.isWorkspaceMutatable(w.id)).map(w => [w.id, w.name]))
  const found = new Map<string, NativeConversationDestination>()
  for (const tab of opts.tabs) {
    const workspaceName = workspaces.get(tab.workspaceId)
    if (workspaceName === undefined || !tab.workerId || !tab.tileId || tab.type === TabType.NATIVE_CONVERSATION)
      continue
    const git = repoGitView(tab, opts.git)
    if (!git.isGitRepo || !git.toplevel)
      continue
    const at = { workspaceId: tab.workspaceId, workerId: tab.workerId, workingDir: git.toplevel }
    const id = nativeConversationPlacementKey(at)
    if (found.has(id))
      continue
    found.set(id, {
      ...at,
      id,
      workspaceName,
      workerName: opts.workerName(tab.workerId) || tab.workerId,
      label: basename(git.toplevel) || git.toplevel,
      ...(git.branchLabel ? { branchName: git.branchLabel } : {}),
      ...(!opts.workerOnline(tab.workerId) ? { disabledReason: 'This computer is offline.' } : {}),
    })
  }
  return [...found.values()].sort((a, b) => a.workspaceName.localeCompare(b.workspaceName)
    || a.label.localeCompare(b.label) || a.workerName.localeCompare(b.workerName) || a.id.localeCompare(b.id))
}
