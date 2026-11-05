import type { Component } from 'solid-js'
import type { BranchRefActions } from './branchActions'
import type { WorkspaceStartActions } from './workspaceStartActions'
import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { TabType, Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'

import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab, TabItemOps } from '~/stores/tab.types'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { createEffect, createMemo, For, Show } from 'solid-js'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { DiffStatsBadge, LabelWithDiffStats } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { sidebarActions } from '../tree/sidebarActions.css'
import { isWorkspaceExpanded, setWorkspacesExpanded, toggleWorkspaceExpanded } from './expandedWorkspaces'
import { WorkspaceContextMenu } from './WorkspaceContextMenu'
import * as styles from './workspaceList.css'
import {
  isSectionFilterShown,
  sectionFilterQuery,
  setSectionFilterQuery,
  toggleSectionFilter,
} from './workspaceListState'
import { RolledUpNotificationDot, WorkspaceTabTree } from './WorkspaceTabTree'
import { sumDiffStatsFromTabs } from './workspaceTabTree.model'

export interface WorkspaceSectionContentProps {
  workspaces: readonly Workspace[]
  sectionId: string
  /** The section's display name, for each row menu's info block. */
  sectionName: string
  activeWorkspaceId: string | null
  sections: Section[]
  onSelect: (id: string) => void
  onRename: (workspace: Workspace) => void
  onMoveTo: (workspaceId: string, targetSectionId: string) => void
  onArchive: (workspaceId: string) => void
  onUnarchive: (workspaceId: string) => void
  onDelete: (workspaceId: string) => void
  isArchived: (workspaceId: string) => boolean
  renamingWorkspaceId: string | null
  renameValue: string
  onRenameInput: (value: string) => void
  onRenameCommit: () => void
  onRenameCancel: () => void
  isWorkspaceLoading: (id: string) => boolean
  getTabsForWorkspace: (workspaceId: string) => Tab[]
  getActiveTabKeyForWorkspace: (workspaceId: string) => string | null
  /**
   * Tile ids in their top-left-first traversal order for the given
   * workspace. Drives the in-tree leaf ordering so it tracks the live
   * tiling layout. Returns `[]` when no layout is projected yet (the CRDT
   * bootstrap hasn't landed); the tree falls back to position-only order in
   * that case.
   */
  getTileOrderForWorkspace: (workspaceId: string) => readonly string[]
  onTabClick: (type: TabType, id: string) => void
  tabItemOps?: TabItemOps
  // No `readOnly` prop: this component derives it per workspace from
  // `isArchived` below, so no caller can hand one row a value that disagrees
  // with its own archived state. The prop it used to forward had no caller at
  // all -- a leftover from workspace sharing, when a workspace could be
  // read-only without being archived.
  /**
   * Reactive lookup for worker display info. Forwarded to
   * {@link WorkspaceTabTree} to disambiguate same-name branches that
   * collide across distinct workers or working directories.
   */
  workerInfoFn?: (id: string) => WorkerInfo | null
  isWorkerKnownOnline?: (workerId: string) => boolean
  /**
   * Whether a worker runs on THIS machine, so the row menu may offer the two
   * actions that open the LOCAL Finder and the LOCAL editor. See
   * `~/lib/workerLocality`.
   */
  isLocalWorkerFn: (workerId: string) => boolean
  /** Open a new agent / terminal at one of a workspace's checkouts. */
  startActions?: WorkspaceStartActions
  /** Branch-menu callbacks, unbound. Each branch row binds them to its own ref. */
  branchActions?: BranchRefActions
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const WorkspaceSectionContent: Component<WorkspaceSectionContentProps> = (props) => {
  // Stable workspace IDs preserve each row, menu anchor and nested tab tree
  // when a projection refresh replaces its workspace objects.

  // Which rows are expanded is APP state, not this instance's: one sidebar
  // mounts this component once per workspace section, and a custom workspace
  // section can sit on the other sidebar.
  // `~/components/workspace/expandedWorkspaces` owns the signal and the
  // sessionStorage write for all of them -- a per-instance signal wrote the
  // whole set back with no merge and erased every other instance's rows.

  // The active workspace when it has a tab, and null otherwise. A memo, so the
  // effect below runs only when this VALUE changes: another workspace becomes
  // active, or the active one gains its first tab. `getTabsForWorkspace` reads
  // one tab projection for every workspace, and it returns a new list whenever
  // any tab changes -- an agent that reports its startup, a title, a git
  // state. An effect that read the list directly expanded the active row again
  // on each such change, and so undid the user's own collapse ("Collapse all",
  // the chevron) a moment after it.
  const activeWorkspaceWithTabs = createMemo((): string | null => {
    const activeId = props.activeWorkspaceId
    if (!activeId)
      return null
    return props.getTabsForWorkspace(activeId).length > 0 ? activeId : null
  })

  // Auto-expand the active workspace when it changes (if it has tabs).
  createEffect(() => {
    const activeId = activeWorkspaceWithTabs()
    if (activeId)
      setWorkspacesExpanded([activeId], true)
  })

  // The active workspace used to be forked onto separate `tabs` /
  // `activeTabKey` props "for backwards compatibility". Both were wired to the
  // very same call at the very same argument as the per-workspace lookups
  // (`view.forWorkspace(activeWorkspaceId)`), so the fork chose between two
  // identical answers. Every workspace is live in the projection now; there is
  // no active/inactive distinction left to make here.
  function tabsFor(workspaceId: string): Tab[] {
    return props.getTabsForWorkspace(workspaceId)
  }

  function activeTabKeyFor(workspaceId: string): string | null {
    return props.getActiveTabKeyForWorkspace(workspaceId)
  }

  /** Per-workspace diff stats. */
  function workspaceDiffStatsFor(workspaceId: string) {
    return sumDiffStatsFromTabs(tabsFor(workspaceId), props.repoGitStore)
  }

  const workspaceIds = () => props.workspaces.map(w => w.id)

  const workspaceById = createMemo(() => {
    const map = new Map<string, Workspace>()
    for (const w of props.workspaces) map.set(w.id, w)
    return map
  })

  return (
    <div
      class={styles.sectionItems}
    >
      {/* In the section BODY, not in the header menu's popover: that keeps
          the menu a plain `menu` (a `div` popover would force a per-item
          close) and leaves the box usable while the menu is shut. */}
      <Show when={isSectionFilterShown(props.sectionId)}>
        <div class={styles.filterRow}>
          <input
            type="text"
            class={styles.filterInput}
            aria-label={`Filter ${props.sectionName}`}
            data-testid={`workspace-filter-${props.sectionId}`}
            placeholder="Filter workspaces"
            value={sectionFilterQuery(props.sectionId) ?? ''}
            onInput={e => setSectionFilterQuery(props.sectionId, e.currentTarget.value)}
            onKeyDown={(e) => {
              // Escape closes the box rather than reaching the sidebar, which
              // would leave a narrowed list with no visible reason for it.
              if (e.key === 'Escape') {
                e.stopPropagation()
                toggleSectionFilter(props.sectionId)
              }
            }}
            ref={(el) => {
              requestAnimationFrame(() => el.focus())
            }}
          />
        </div>
      </Show>
      <Show
        when={props.workspaces.length > 0}
        fallback={(
          <div class={styles.emptySection}>
            {isSectionFilterShown(props.sectionId) && (sectionFilterQuery(props.sectionId) ?? '') !== ''
              ? 'No matching workspaces'
              : 'No workspaces'}
          </div>
        )}
      >
        <For each={workspaceIds()}>
          {(id) => {
            const workspace = () => workspaceById().get(id)!

            const isActive = () => id === props.activeWorkspaceId
            const isRenaming = () => props.renamingWorkspaceId === id
            const isLoading = () => props.isWorkspaceLoading(id)
            const title = () => workspace().title || 'Untitled'
            // workspaceDiffStatsFor sums diff stats across the
            // workspace's tabs; memoize so we don't re-sum on every
            // access.
            const stats = createMemo(() => workspaceDiffStatsFor(id))

            // The row element, for the right-click / long-press menu below.
            const [rowEl, setRowEl] = createContextMenuAnchor()

            return (
              <>
                <div
                  ref={setRowEl}
                  class={styles.item}
                  classList={{
                    [styles.itemActive]: isActive(),
                  }}
                  onClick={() => props.onSelect(id)}
                  onDblClick={() => props.onRename(workspace())}
                  data-testid={`workspace-item-${id}`}
                  // Which workspace is active is no longer in the URL, so this
                  // row is the only place it is observable from outside. E2E
                  // asserts a switch landed on it; the styling still comes
                  // from `itemActive` above.
                  data-active={isActive() ? 'true' : 'false'}
                  // Collapsing only sets `visibility: hidden` on the children
                  // wrapper, so the leaves stay in the DOM and counting them
                  // cannot tell expanded from collapsed. Expose the bit.
                  data-expanded={isWorkspaceExpanded(id) ? 'true' : 'false'}
                >
                  <ChevronRight
                    size={14}
                    class={`${shared.chevron} ${isWorkspaceExpanded(id) ? shared.chevronExpanded : ''}`}
                    data-testid={`workspace-chevron-${id}`}
                    onClick={(e) => {
                      e.stopPropagation()
                      toggleWorkspaceExpanded(id)
                    }}
                  />
                  <Show
                    when={!isRenaming()}
                    fallback={(
                      <input
                        class={styles.itemRenameInput}
                        value={props.renameValue}
                        onInput={e => props.onRenameInput(e.currentTarget.value)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') {
                            e.preventDefault()
                            props.onRenameCommit()
                          }
                          if (e.key === 'Escape')
                            props.onRenameCancel()
                        }}
                        onBlur={() => props.onRenameCommit()}
                        onClick={e => e.stopPropagation()}
                        ref={(el) => {
                          requestAnimationFrame(() => {
                            el.focus()
                            el.select()
                          })
                        }}
                      />
                    )}
                  >
                    <Tooltip content={<LabelWithDiffStats label={title()} stats={stats()} />} showWhen="clipped">
                      <span class={styles.itemLabel}>
                        {/* The E2E suite clicks here to select the workspace. A long title makes the row wider than
                            the sidebar, and the center of such a row lies under the three-dot trigger, which
                            stays pinned to the visible edge. */}
                        <span class={styles.itemTitle} data-testid="workspace-title">{title()}</span>
                        <DiffStatsBadge stats={stats()} />
                      </span>
                    </Tooltip>
                  </Show>
                  <RolledUpNotificationDot
                    folded={() => !isWorkspaceExpanded(id)}
                    tabs={() => tabsFor(id)}
                  />

                  <div class={sidebarActions}>
                    <Show
                      when={!isLoading()}
                      fallback={<Spinner size="xs" />}
                    >
                      <Show when={!isRenaming()}>
                        <WorkspaceContextMenu
                          contextMenuFor={rowEl}
                          workspaceId={id}
                          workspaceTitle={title()}
                          sectionName={props.sectionName}
                          isArchived={props.isArchived(id)}
                          sections={props.sections}
                          currentSectionId={props.sectionId}
                          getTabs={() => tabsFor(id)}
                          repoGitStore={props.repoGitStore}
                          {...(props.workerInfoFn !== undefined ? { workerInfoFn: props.workerInfoFn } : {})}
                          {...(props.isWorkerKnownOnline !== undefined ? { isWorkerOnline: props.isWorkerKnownOnline } : {})}
                          isLocalWorkerFn={props.isLocalWorkerFn}
                          {...(props.startActions !== undefined ? { startActions: props.startActions } : {})}
                          onRename={() => props.onRename(workspace())}
                          onMoveTo={targetSectionId => props.onMoveTo(id, targetSectionId)}
                          onArchive={() => props.onArchive(id)}
                          onUnarchive={() => props.onUnarchive(id)}
                          onDelete={() => props.onDelete(id)}
                        />
                      </Show>
                    </Show>
                  </div>
                </div>
                <div
                  class={`${shared.childrenWrapper} ${isWorkspaceExpanded(id) ? shared.childrenWrapperExpanded : ''}`}
                  data-testid={`workspace-children-${id}`}
                >
                  <div class={shared.childrenInner}>
                    <WorkspaceTabTree
                      tabs={tabsFor(id)}
                      tileOrder={props.getTileOrderForWorkspace(id)}
                      activeTabKey={activeTabKeyFor(id)}
                      onTabClick={(type, tabId) => {
                        // Select first, then switch. Both orders work now —
                        // every workspace's tabs are in the projection, so the
                        // clicked tab is selectable before its workspace is on
                        // screen. This used to hand the choice off through
                        // sessionStorage for the restore path to pick up,
                        // because the tab did not exist in any live store
                        // until the switch had finished loading it.
                        props.onTabClick(type, tabId)
                        if (id !== props.activeWorkspaceId)
                          props.onSelect(id)
                      }}
                      {...(props.tabItemOps !== undefined ? { tabItemOps: props.tabItemOps } : {})}
                      // Per WORKSPACE, not per sidebar: this section lists
                      // several, and only some of them are archived. The tab
                      // bar derives its own copy the same way, from
                      // `isActiveWorkspaceArchived`, and these are the same
                      // tabs it renders.
                      archived={props.isArchived(id)}
                      workspaceId={id}
                      {...(props.workerInfoFn !== undefined ? { workerInfoFn: props.workerInfoFn } : {})}
                      {...(props.isWorkerKnownOnline !== undefined ? { isWorkerKnownOnline: props.isWorkerKnownOnline } : {})}
                      isLocalWorkerFn={props.isLocalWorkerFn}
                      {...(props.branchActions !== undefined ? { branchActions: props.branchActions } : {})}
                      repoGitStore={props.repoGitStore}
                    />
                  </div>
                </div>
              </>
            )
          }}
        </For>
      </Show>
    </div>
  )
}
