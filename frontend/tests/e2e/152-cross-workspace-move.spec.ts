import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { createWorkspaceViaAPI } from './helpers/api'
import { withExtraClients } from './helpers/multiClient'
import { clickRowMenuItem, expandWorkspaceRow, expectAgentTabCount, gotoWorkspace, sidebarLeafIds, sidebarLeaves, sidebarSectionHeader, tiles, visibleOnly, waitForLayoutSave, waitForWorkspaceReady, workspaceRow } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'
import { moveSidebarTabToWorkspace } from './helpers/workspaceMoveMenu'

async function clickSectionMenuItem(page: Page, slug: string, itemName: string): Promise<void> {
  const trigger = visibleOnly(page.getByTestId(`sidebar-section-menu-${slug}`)).first()
  const item = page.getByTestId(`sidebar-section-menu-${slug}-popover`).getByRole('menuitem', { name: itemName, exact: true })
  await clickRowMenuItem(null, trigger, item)
}

/**
 * Cross-workspace tab move convergence and workspace isolation.
 *
 * The plan's invariant: a cross-workspace tab move is one CRDT op
 * batch (`SetTabRegister(tile_id=newTileInW2)` + position). The hub
 * resolves the new owning workspace via the new tile's ancestor
 * chain. Source-only subscribers see `EntityRemoved`; destination-
 * only subscribers see `EntityMaterialized`. Both views update
 * without flickering through a "no workspace" intermediate state.
 *
 * The explicit Move to workspace menu's CRDT contract — a single
 * `SetTabRegister(tile_id=newTileInW2)` + `SetTabRegister(position)`
 * batch instead of tombstone-then-re-add — is exercised at the unit
 * level in `src/stores/tab.store.crdt.test.ts`
 * (`moveTabToWorkspace emits a single batch with tile_id + position`).
 * The menu flow below uses actual opened agents and independent clients
 * viewing the source and destination, then reloads the destination.
 *
 * This spec covers:
 *
 *   1. Each workspace's projection is independent — a layout edit in
 *      W1 does not reach a client viewing W2.
 *   2. The lifecycle event for a freshly-created workspace propagates
 *      to all userevents subscribers via the `/ws/userevents` stream.
 */

test.describe('Cross-workspace projection isolation', () => {
  test('a sidebar label menu survives an unrelated pane scroll and still offers Move to workspace', async ({ page, leapmuxServer }) => {
    const { workspaceId: source } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Label menu source')
    await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Label menu target')
    await gotoWorkspace(page, leapmuxServer.adminToken, source)
    await expandWorkspaceRow(page, source)
    const leaf = sidebarLeaves(page, source).first()
    await leaf.getByTestId('tab-tree-leaf-label').click({ button: 'right' })
    const menu = leaf.getByTestId('tab-tree-leaf-menu')
    await expect(menu).toBeVisible()

    // A real browser scroll event from another pane must not dismiss a menu
    // whose owning sidebar row remains in place. The temporary pane contains
    // only generated geometry and never changes workspace or chat data.
    await page.evaluate(() => new Promise<void>((resolve) => {
      const pane = document.createElement('div')
      pane.style.cssText = 'position:fixed;right:0;bottom:0;width:20px;height:20px;overflow:auto;pointer-events:none'
      const content = document.createElement('div')
      content.style.height = '200px'
      pane.append(content)
      document.body.append(pane)
      pane.addEventListener('scroll', () => {
        requestAnimationFrame(() => requestAnimationFrame(() => {
          pane.remove()
          resolve()
        }))
      }, { once: true })
      pane.scrollTop = 40
    }))

    await expect(menu).toBeVisible()
    await expect(menu.getByTestId('tab-menu-move')).toBeVisible()
    await expect(menu.getByTestId('tab-menu-move')).toHaveText('Move to workspace')
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
  })

  test('a menu move converges across source and destination clients and survives reload', async ({ browser, leapmuxServer }, testInfo) => {
    const { workspaceId: source, agentIds: [sourceAgent] } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Menu source')
    const { workspaceId: target, agentIds: [targetAgent] } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Menu target')
    if (!sourceAgent || !targetAgent)
      throw new Error('Both workspaces need their opened agent.')
    await withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      await Promise.all([
        gotoWorkspace(pageA, leapmuxServer.adminToken, source),
        gotoWorkspace(pageB, leapmuxServer.adminToken, target),
      ])
      await expandWorkspaceRow(pageA, source)
      await expect.poll(() => sidebarLeafIds(pageA, source)).toEqual([sourceAgent])
      await expect.poll(() => sidebarLeafIds(pageB, target)).toEqual([targetAgent])

      const saved = waitForLayoutSave(pageA)
      await moveSidebarTabToWorkspace(pageA, sidebarLeaves(pageA, source).first(), target, testInfo.outputPath('move-to-workspace-menu.png'))
      await saved
      await expandWorkspaceRow(pageA, target)
      await expect.poll(() => sidebarLeafIds(pageA, target)).toEqual([targetAgent, sourceAgent])
      await expect.poll(() => sidebarLeafIds(pageA, source)).toEqual([])
      await expect.poll(() => sidebarLeafIds(pageB, target)).toEqual([targetAgent, sourceAgent])
      await expectAgentTabCount(pageB, 2)

      await pageB.reload()
      await waitForWorkspaceReady(pageB)
      await expect.poll(() => sidebarLeafIds(pageB, target)).toEqual([targetAgent, sourceAgent])
      await expandWorkspaceRow(pageB, source)
      await expect.poll(() => sidebarLeafIds(pageB, source)).toEqual([])
    })
  })

  test('a layout edit in W1 does not reach a client viewing W2', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    // The suite reset deletes these workspaces, and the marker below, before the next test.
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-W1')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-W2')
    await withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, ws1),
        gotoWorkspace(pageB, adminToken, ws2),
      ])

      // Both workspaces start with one tile.
      await expect(tiles(pageA)).toHaveCount(1)
      await expect(tiles(pageB)).toHaveCount(1)

      // Split in W1 — W2's view must remain a single tile. The hub commits
      // the split before the layout-saved event of the client that sent it.
      const saved = waitForLayoutSave(pageA)
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(tiles(pageA)).toHaveCount(2)
      await saved

      // Then a marker event that reaches every subscriber: a new workspace.
      // The hub sends each subscriber its events in commit order, so B
      // receives any cross-talk from the split before the marker, and the
      // marker row in B's sidebar ends the window.
      const marker = await createWorkspaceViaAPI(hubUrl, adminToken, 'iso-marker')
      await expect(workspaceRow(pageB, marker), 'the marker event reached B after the split').toBeVisible()
      await expect(tiles(pageB)).toHaveCount(1)
    })
  })

  test('a workspace created in one client appears in another client subscribed to userevents', async ({ page, emptyWorkspace, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    await gotoWorkspace(page, adminToken, emptyWorkspace.workspaceId)

    // Create a sibling workspace via the hub API; the userevents WS
    // stream should deliver `WorkspaceCreated` and the sidebar
    // should pick it up. The sidebar's row is keyed off the
    // workspace list, which the UserCRDT lifecycle events feed.
    const newWsTitle = 'sibling-via-userevents-stream'
    const newWsId = await createWorkspaceViaAPI(hubUrl, adminToken, newWsTitle)
    await expect(workspaceRow(page, newWsId)).toContainText(newWsTitle)
  })

  test('a workspace still moves to a section through its right-click menu', async ({ page, emptyWorkspace, leapmuxServer }) => {
    const workspaceId = emptyWorkspace.workspaceId
    await gotoWorkspace(page, leapmuxServer.adminToken, workspaceId)
    await clickSectionMenuItem(page, 'workspaces_in_progress', 'New section...')
    const dialog = visibleOnly(page.getByTestId('section-name-dialog')).first()
    await dialog.getByTestId('title-input').fill('Moved workspaces')
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    const custom = sidebarSectionHeader(page, 'workspaces_custom')
    await expect(custom).toContainText('Moved workspaces')

    await workspaceRow(page, workspaceId).click({ button: 'right' })
    await workspaceRow(page, workspaceId).getByTestId('workspace-move-to').click()
    const destinations = visibleOnly(page.getByTestId('workspace-move-to-popover')).first()
    await destinations.getByRole('menuitem', { name: 'Moved workspaces', exact: true }).click()
    await expect(custom.locator('..').getByTestId(`workspace-item-${workspaceId}`)).toBeVisible()

    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(custom.locator('..').getByTestId(`workspace-item-${workspaceId}`)).toBeVisible()
    await clickSectionMenuItem(page, 'workspaces_custom', 'Delete section...')
    await page.locator('dialog:visible').getByRole('button', { name: 'Delete', exact: true }).click()
    await page.locator('dialog:visible').getByRole('button', { name: 'Confirm?', exact: true }).click()
    await expect(custom).toHaveCount(0)
  })
})
