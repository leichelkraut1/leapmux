import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { appMenuTrigger } from './helpers/ui'

/**
 * The Trading Research view against the suite's real Worker, in paper mode
 * and with no broker credentials. The lab's settings, synthetic market data,
 * broker verification and books round-trip through the encrypted channel.
 * No run is launched, so no provider starts and the model is never asked.
 */
async function openTrading(page: Page, workerId: string): Promise<Locator> {
  await appMenuTrigger(page).click()
  await page.getByRole('menuitem', { name: 'Workflows', exact: true }).click()
  const dialog = page.getByTestId('workflows-dialog')
  await expect(dialog).toBeVisible()
  await dialog.getByRole('radio', { name: 'Trading research', exact: true }).click()
  const view = dialog.getByTestId('trading-research')
  await view.getByTestId('trading-lab-computer').click()
  await page.getByTestId('trading-lab-computer-menu').getByTestId(`workflow-computer-option-${workerId}`).click()
  return view
}

test.describe('Trading research', () => {
  test('runs paper-only with synthetic data and no broker credentials', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }, testInfo) => {
    void authenticatedEmptyWorkspace
    const view = await openTrading(page, leapmuxServer.workerId)

    await view.getByRole('radio', { name: 'Settings', exact: true }).click()
    const settings = view.getByTestId('trading-settings')
    await expect(settings.getByRole('radio', { name: 'Paper only', exact: true })).toBeChecked()
    await settings.getByTestId('trading-install-sample').click()
    await expect(settings.getByTestId('trading-replay-dir')).toHaveValue(/synthetic-replay/)

    // No server is configured: verification says so and live mode stays shut.
    await settings.getByTestId('trading-verify-broker').click()
    const broker = settings.getByTestId('trading-broker-status')
    await expect(broker).toContainText('Not authenticated')
    await expect(broker).toContainText('Not ready for live orders')
    await settings.getByRole('radio', { name: 'Live (confirmed orders)', exact: true }).click()
    await settings.getByTestId('trading-save-settings').click()
    await expect(settings.getByRole('alert')).toContainText('live mode needs')

    await view.getByRole('radio', { name: 'Queue and orders', exact: true }).click()
    const orders = view.getByTestId('trading-orders')
    await expect(orders).toContainText('Paper mode')
    await expect(orders.getByTestId('trading-order-row')).toHaveCount(0)

    await view.getByRole('radio', { name: 'Portfolios', exact: true }).click()
    const portfolio = view.getByTestId('trading-portfolio')
    await expect(portfolio.getByTestId('trading-book-card')).toHaveCount(4)
    await expect(portfolio).toContainText('Blind, opposite direction')
    await expect(portfolio).toContainText('Live (real fills, reconciled from the broker)')
    await view.screenshot({ path: testInfo.outputPath('trading-research-desktop.png') })
  })

  test('installs the blind-trading-lab template and launches it without a Lead', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    void authenticatedEmptyWorkspace
    await appMenuTrigger(page).click()
    await page.getByRole('menuitem', { name: 'Workflows', exact: true }).click()
    const dialog = page.getByTestId('workflows-dialog')
    await dialog.getByRole('radio', { name: 'Templates', exact: true }).click()
    await dialog.getByTestId('workflow-library-computer').click()
    await page.getByTestId('workflow-library-computer-menu').getByTestId(`workflow-computer-option-${leapmuxServer.workerId}`).click()
    await dialog.getByTestId('workflow-add-example-blind-trading-lab').click()
    const row = dialog.getByTestId('workflow-template-row').filter({ has: page.getByRole('heading', { name: 'Blind trading research', exact: true }) }).first()
    await expect(row).toBeVisible()
    await row.getByRole('button', { name: 'Launch', exact: true }).click()
    const form = dialog.getByTestId('workflow-launch-form')
    await expect(form).toContainText('Coordinator')
    await expect(form).not.toContainText('This template has no Lead role.')
    // Launching would start the analysts' agents; the engine path is covered
    // by the Go flow test, which drives the same template to completion.
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await row.getByRole('button', { name: 'Delete', exact: true }).click()
    await row.getByRole('button', { name: 'Delete?', exact: true }).click()
  })
})
