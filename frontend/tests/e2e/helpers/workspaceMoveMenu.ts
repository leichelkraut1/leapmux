import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { visibleOnly } from './ui'

/** Move a sidebar tab through its own right-click menu and a workspace ID. */
export async function moveSidebarTabToWorkspace(page: Page, leaf: Locator, workspaceId: string, screenshotPath?: string): Promise<void> {
  await leaf.click({ button: 'right' })
  const menu = leaf.getByTestId('tab-tree-leaf-menu')
  await expect(menu).toBeVisible()
  await menu.getByTestId('tab-menu-move').click()
  const destinations = visibleOnly(page.getByTestId('tab-menu-move-popover')).first()
  await expect(destinations).toBeVisible()
  if (screenshotPath)
    await page.screenshot({ path: screenshotPath, fullPage: true })
  await destinations.locator(`[data-testid="tab-menu-move-target"][data-workspace-id="${workspaceId}"]`).click()
}
