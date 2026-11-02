import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { nativeConversationFixture } from './helpers/nativeConversationFixture'
import { withNativeWorker } from './helpers/nativeWorker'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

for (const provider of ['codex', 'claude'] as const) {
  test(`links saved ${provider} chat across computers, restores it and unlinks without touching the source`, async ({ page, leapmuxServer }, testInfo) => {
    const fixture = nativeConversationFixture(provider)
    const title = `Generated linked ${provider} workspace`
    const repo = createGitRepo(leapmuxServer.dataDir, `linked-${provider}-checkout`)
    const workspace = await showWorkspaceWithAgents(page, leapmuxServer, title, { workingDir: repo })
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await withNativeWorker(leapmuxServer, {
      workerName: `Generated source ${provider} computer`,
      dataDirPrefix: 'linked-chat-source-',
      env: fixture.env,
      privateDirectories: [fixture.home],
    }, async ({ workerId }) => {
      expect(workerId).not.toBe(leapmuxServer.workerId)
      const query = new URLSearchParams({ dashboard: 'agents', externalWorker: workerId, externalProvider: provider, externalSession: fixture.sessionId })
      const link = page.locator('[data-testid="tab"][data-tab-type="native-conversation"]')
      const attach = async () => {
        await page.goto(`/?${query}`)
        const viewer = page.getByTestId('native-agent-transcript')
        await expect(viewer).toBeVisible()
        await expect(viewer.getByText('GENERATED_LATEST_HISTORY_MARKER', { exact: true })).toBeVisible()
        await viewer.getByRole('button', { name: 'Add to worktree', exact: true }).click()
        const chooser = page.getByTestId('attach-native-conversation-dialog')
        await expect(chooser).toBeVisible()
        await chooser.getByRole('button', { name: 'Workspace', exact: true }).click()
        await page.getByRole('menuitemradio', { name: title, exact: true }).click()
        await expect(chooser.getByText(repo, { exact: false })).toBeVisible()
        await chooser.getByRole('button', { name: 'Add to worktree', exact: true }).click()
        await expect(chooser).toHaveCount(0)
        await expect(viewer).toHaveCount(0)
        await expect(link).toHaveCount(1)
      }
      await attach()
      const tabId = await link.getAttribute('data-tab-id')
      expect(tabId).toBeTruthy()
      await expect(link.getByText('Linked', { exact: true })).toBeVisible()
      await expect(page.getByText('GENERATED_LATEST_HISTORY_MARKER', { exact: true })).toBeVisible()
      await expect(page.getByText(`Generated source ${provider} computer`, { exact: false }).first()).toBeVisible()
      expect(fixture.unchanged()).toBe(true)
      await page.reload()
      await expect(link).toHaveCount(1)
      await link.click()
      await expect(link).toHaveAttribute('data-tab-id', tabId!)
      await expect(page.getByText('GENERATED_LATEST_HISTORY_MARKER', { exact: true })).toBeVisible()
      await attach()
      await expect(link).toHaveAttribute('data-tab-id', tabId!)
      await page.screenshot({ path: testInfo.outputPath(`${provider}-linked-chat.png`), fullPage: true })
      await link.getByTestId('tab-close').click()
      await expect(link).toHaveCount(0)
      await expect(page.getByRole('dialog')).toHaveCount(0)
      expect(existsSync(repo)).toBe(true)
      expect(fixture.unchanged()).toBe(true)
      // The native history remains openable after the link was removed.
      await page.goto(`/?${query}`)
      await expect(page.getByTestId('native-agent-transcript').getByText('GENERATED_LATEST_HISTORY_MARKER', { exact: true })).toBeVisible()
      expect(errors).toEqual([])
      expect(workspace.workspaceId).toBeTruthy()
    })
  })
}
