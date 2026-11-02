import type { Tab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { nativeConversationDestinations, nativeConversationMatches, nativeConversationPlacementAnchor, nativeConversationPlacementKey } from './nativeConversationPlacement'

describe('native conversation worktree placement', () => {
  it('groups existing checkout tabs by workspace and destination PC and excludes archived/unresolved tabs', () => {
    createRoot((dispose) => {
      try {
        const git = createRepoGitStore()
        for (const workerId of ['windows', 'mac'])
          git.upsert(repoKey(workerId, '/generated/tree'), { workerId, repoRoot: '/generated/tree', toplevel: '/generated/tree', branch: 'fixture' })
        const tab = (id: string, workspaceId: string, workerId: string): Tab => ({ id, workspaceId, workerId, tileId: `tile-${id}`, type: TabType.TERMINAL, gitToplevel: '/generated/tree' })
        const tabs = [tab('a', 'open', 'windows'), tab('b', 'open', 'windows'), tab('c', 'open', 'mac'), tab('d', 'archived', 'mac'), { ...tab('e', 'open', 'unknown'), gitToplevel: undefined }]
        const destinations = nativeConversationDestinations({ tabs, workspaces: [{ id: 'open', name: 'Open' }, { id: 'archived', name: 'Archive' }], git, isWorkspaceMutatable: id => id === 'open', workerOnline: id => id === 'windows', workerName: id => id })
        expect(destinations).toHaveLength(2)
        expect(destinations.find(d => d.workerId === 'mac')?.disabledReason).toContain('offline')
        expect(destinations.find(d => d.workerId === 'windows')).toMatchObject({ workspaceId: 'open', workingDir: '/generated/tree', branchName: 'fixture' })
        expect(nativeConversationPlacementAnchor(tabs, { workspaceId: 'open', workerId: 'windows', workingDir: '/generated/tree' }, git)?.id).toBe('a')
        expect(nativeConversationPlacementAnchor(tabs, { workspaceId: 'open', workerId: 'windows', workingDir: '/other' }, git)).toBeUndefined()
      }
      finally { dispose() }
    })
  })

  it('deduplicates by source conversation and destination, never by title or destination worker as source', () => {
    const ref = { workerId: 'source-mac', workerName: 'Mac', agentProvider: AgentProvider.CODEX, sessionId: 'c3eda383-638f-46df-a5fc-83f6cecd1a92' }
    const at = { workspaceId: 'workspace', workerId: 'destination-windows', workingDir: '/generated/tree' }
    const tab: Tab = { id: 'native-fixture', ...at, type: TabType.NATIVE_CONVERSATION, sourceWorkerId: ref.workerId, agentProvider: ref.agentProvider, sessionId: ref.sessionId }
    expect(nativeConversationMatches(tab, ref, at)).toBe(true)
    expect(nativeConversationMatches(tab, { ...ref, workerId: 'another-pc' }, at)).toBe(false)
    expect(nativeConversationMatches(tab, ref, { ...at, workingDir: '/another/tree' })).toBe(false)
    expect(nativeConversationMatches(tab, ref, { ...at, workspaceId: 'another-workspace' })).toBe(false)
  })

  it('uses the same Windows checkout identity for link matches, pending keys, anchors and destinations', () => {
    createRoot((dispose) => {
      try {
        const ref = { workerId: 'source-mac', workerName: 'Mac', agentProvider: AgentProvider.CLAUDE_CODE, sessionId: 'generated-source-session' }
        const at = { workspaceId: 'workspace', workerId: 'windows', workingDir: 'C:/Generated/Worktree' }
        const stored = { ...at, workingDir: 'c:\\generated\\worktree' }
        const linked: Tab = { ...stored, id: 'native-fixture', tileId: 'tile', type: TabType.NATIVE_CONVERSATION, sourceWorkerId: ref.workerId, agentProvider: ref.agentProvider, sessionId: ref.sessionId }
        expect(nativeConversationMatches(linked, ref, at)).toBe(true)
        expect(nativeConversationPlacementKey(stored)).toBe(nativeConversationPlacementKey(at))
        const git = createRepoGitStore()
        const tabs: Tab[] = ['C:/Generated/Worktree', 'c:\\generated\\worktree'].map((dir, index) => ({ id: `anchor-${index}`, workspaceId: at.workspaceId, workerId: at.workerId, tileId: 'tile', type: TabType.TERMINAL, gitToplevel: dir }))
        for (const tab of tabs)
          git.upsert(repoKey(at.workerId, tab.gitToplevel!), { workerId: at.workerId, repoRoot: tab.gitToplevel!, toplevel: tab.gitToplevel!, branch: 'fixture' })
        expect(nativeConversationPlacementAnchor(tabs, stored, git)?.id).toBe('anchor-0')
        expect(nativeConversationDestinations({ tabs, workspaces: [{ id: at.workspaceId, name: 'Generated workspace' }], git, isWorkspaceMutatable: () => true, workerOnline: () => true, workerName: id => id })).toHaveLength(1)
      }
      finally { dispose() }
    })
  })

  it('keeps POSIX case and literal backslashes distinct without rewriting any path', () => {
    const ref = { workerId: 'source', workerName: 'Source', agentProvider: AgentProvider.CODEX, sessionId: 'generated-session' }
    const at = { workspaceId: 'workspace', workerId: 'mac', workingDir: '/Generated/Worktree' }
    const tab: Tab = { ...at, id: 'native-fixture', type: TabType.NATIVE_CONVERSATION, sourceWorkerId: ref.workerId, agentProvider: ref.agentProvider, sessionId: ref.sessionId }
    expect(nativeConversationMatches(tab, ref, { ...at, workingDir: '/generated/Worktree' })).toBe(false)
    expect(nativeConversationMatches(tab, ref, { ...at, workingDir: '/Generated\\Worktree' })).toBe(false)
    expect(nativeConversationPlacementKey(at)).not.toBe(nativeConversationPlacementKey({ ...at, workingDir: '/generated/Worktree' }))
    expect(tab.workingDir).toBe('/Generated/Worktree')
  })
})
