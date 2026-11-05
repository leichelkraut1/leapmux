import { expect } from '@playwright/test'
import { test } from './fixtures'
import { boxCenter, mouseDragOnto } from './helpers/drag'
import { settleFrames } from './helpers/frames'
import { tabbarLabels } from './helpers/tabLabels'
import { agentTabs, expandWorkspaceRow, expectAgentTabCount, sidebarLeafIds, sidebarLeafLabels, sidebarLeaves, waitForLayoutSave, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI, showWorkspaceWithAgents } from './helpers/workspace'
import { moveSidebarTabToWorkspace } from './helpers/workspaceMoveMenu'

/**
 * Regression: dragging a tab from a non-active workspace's expanded
 * sidebar section to the active workspace (either onto the active
 * workspace's sidebar item or onto the active tabbar zone) used to
 * strip the tab's `title` and `agentProvider` from the destination,
 * so the sidebar row rendered the agent's nanoid and the tabbar
 * rendered "Agent" with the generic icon. Refreshing the page
 * re-fetched the agent record and the title returned — confirming
 * the data loss was purely client-side.
 *
 * Root cause: the CRDT-projection reconciler effect in `AppShell.tsx`
 * read `tabStore.state.tabs` inside its body without `untrack`. The
 * optimistic `tabStore.addTab` in the cross-workspace move handler
 * re-ran the effect against a CRDT projection that hadn't yet absorbed
 * the move op (the op only ships after the worker RPC resolves). Step
 * 1 silently removed the just-added tab as "gone from this workspace",
 * and step 2 re-added it as a bare record (no title / agentProvider /
 * git fields) after the move op finally landed.
 *
 * Defense layered with `tabStore.addTab` dedupe by `(type, id)` — an
 * HMR / concurrent-restore race could land both a bare reconciler
 * insert and a full worker-restore insert for the same id, producing
 * two sidebar rows that "closing one removes both" because removeTab
 * filters by key. The dedupe makes the first insert (typically the
 * one with metadata) win.
 *
 * The explicit Move to workspace menu uses the same CRDT move. Pointer
 * attempts on workspace/chat rows must leave their order and membership
 * unchanged; the menu move must retain metadata and persist after reload.
 */

test.describe('Cross-workspace sidebar menu move preserves title and icon', () => {
  test('menu move from a non-active workspace keeps its title and survives reload', async ({ page, leapmuxServer }) => {
    // Give each agent a known title. The bug removes exactly this field.
    // An agent that the API opens has an empty title by default, which
    // shows the empty fallback before the move and after it, so the
    // test could not see the regression.
    const wsATitle = 'Source Agent'
    const wsBTitle = 'Target Agent'
    const { workspaceId: wsA, agentIds: [wsAAgentId] } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Move Source', { agentTitles: [wsATitle] })
    if (!wsAAgentId)
      throw new Error('The source workspace opened no agent.')

    // Show wsB, the destination. In the original report, the target
    // workspace was active at the time of the drag.
    const { workspaceId: wsB } = await showWorkspaceWithAgents(page, leapmuxServer, 'Move Target', { agentTitles: [wsBTitle] })
    await agentTabs(page).first().waitFor()
    // Poll: the title is worker-side metadata, fetched asynchronously after
    // the tab itself renders from the CRDT projection. A one-shot read races
    // that fetch. This still fails if the title never arrives.
    await expect.poll(() => tabbarLabels(page, 'agent')).toEqual([wsBTitle])

    // Expand wsA in the sidebar so its tab-tree-leaf mounts and is
    // available. The projection already carries wsA's tabs, so the expansion
    // only shows them.
    await expandWorkspaceRow(page, wsA)
    // One leaf each under wsA (the source) and wsB (the destination --
    // already visible because wsB is active). Counted PER WORKSPACE, not
    // across the whole sidebar: a global `toHaveCount(2)` would also assert
    // that no other workspace is expanded, which this test is not about.
    await expect.poll(() => sidebarLeafLabels(page, wsA)).toHaveLength(1)
    await expect.poll(() => sidebarLeafLabels(page, wsB)).toHaveLength(1)

    // Verify wsA's leaf renders with its seeded title before the
    // move — confirms the projection + hydrators delivered the metadata
    // we'll be asserting survives the move.
    await expect.poll(() => sidebarLeafLabels(page, wsA)).toEqual([wsATitle])

    const sourceLeaf = sidebarLeaves(page, wsA).first()
    await expect(workspaceRow(page, wsA).locator('[data-drag-handle]')).toHaveCount(0)
    await expect(sourceLeaf.locator('[data-drag-handle]')).toHaveCount(0)
    const section = workspaceRow(page, wsA).locator('..')
    const workspaceOrder = () => section.locator('[data-testid^="workspace-item-"]')
      .evaluateAll(rows => rows.map(row => row.getAttribute('data-testid')))
    const beforeOrder = await workspaceOrder()
    const sourceIds = await sidebarLeafIds(page, wsA)
    const targetIds = await sidebarLeafIds(page, wsB)

    // Real pointer travel exceeds the drag activation distance. Neither the
    // workspace row nor the chat leaf may lift, reorder or change ownership.
    await mouseDragOnto(page, {
      from: await boxCenter(workspaceRowTitle(page, wsA)),
      to: await boxCenter(workspaceRowTitle(page, wsB)),
      whileLifted: async () => expect(workspaceRow(page, wsA)).not.toHaveClass(/itemDragging/),
    })
    await settleFrames(page)
    expect(await workspaceOrder()).toEqual(beforeOrder)
    await mouseDragOnto(page, {
      from: await boxCenter(sourceLeaf),
      to: await boxCenter(workspaceRowTitle(page, wsB)),
      whileLifted: async () => expect(sourceLeaf).not.toHaveClass(/tabDragging/),
    })
    await settleFrames(page)
    expect(await workspaceOrder()).toEqual(beforeOrder)
    expect(await sidebarLeafIds(page, wsA)).toEqual(sourceIds)
    expect(await sidebarLeafIds(page, wsB)).toEqual(targetIds)

    // The deliberate menu choice performs the non-active → active move.
    const saved = waitForLayoutSave(page)
    await moveSidebarTabToWorkspace(page, sourceLeaf, wsB)
    await saved

    // wsB's tabbar now has two agent tabs — the original wsBTitle
    // and the moved wsATitle. Both must keep their titles; the
    // pre-fix bug would have collapsed the moved one to a bare
    // "Agent" (tabbar fallback) and its sidebar row to the nanoid.
    await expectAgentTabCount(page, 2)
    const tabbarTitles = await tabbarLabels(page, 'agent')
    expect(new Set(tabbarTitles)).toEqual(new Set([wsATitle, wsBTitle]))
    expect(tabbarTitles).not.toContain(wsAAgentId)
    expect(tabbarTitles).not.toContain('Agent')

    // Mirror assertion in the sidebar — wsB's section now lists
    // both tabs and neither row shows the nanoid fallback.
    const sidebarLabels = await sidebarLeafLabels(page, wsB)
    expect(new Set(sidebarLabels)).toEqual(new Set([wsATitle, wsBTitle]))

    // --- Reload checkpoint (#3) ---
    //
    // The cross-workspace move emits a CRDT `SetTabRegister(tile_id)`
    // batch and nothing else — the worker stores no workspace id, so
    // there is no worker-side RPC to pair it with. After reload, the
    // batch should be durably committed — the destination workspace
    // must still show two tabs (the original + the moved), and the
    // moved tab id must NOT have leaked back to the source.
    //
    // We intentionally only assert COUNT and id-NOT-leaked-to-source
    // here, not titles. Title round-tripping through `listAgents`
    // after a refresh interacts with several worker-side concerns
    // (openAgent title persistence, listAgents ordering vs the
    // CRDT-projection reconciler) that are independent of the bug
    // under test. A non-deterministic title result post-reload
    // would mask the actual move regression we DO cover (the
    // pre-reload assertion above).
    await page.reload()
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()

    await expectAgentTabCount(page, 2)
    // The moved tab is under wsB in the reloaded sidebar. This positive read
    // comes first: it proves that the sidebar projected the reloaded layout,
    // so the empty read of wsA below cannot pass on a tree that never rendered.
    await expect.poll(() => sidebarLeafIds(page, wsB)).toContain(wsAAgentId)
    // wsAAgentId must not appear back under wsA's sidebar section
    // after refresh — the move op committed to the hub, so the CRDT
    // projection that the reload builds no longer places it under wsA. The
    // reload restores wsA as expanded, so a bare chevron click would collapse it.
    await expandWorkspaceRow(page, wsA)
    expect(await sidebarLeafIds(page, wsA)).toEqual([])
  })
})
