import type { Locator, Page } from '@playwright/test'
import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { nativeConversationFixture } from './helpers/nativeConversationFixture'
import { withNativeWorker } from './helpers/nativeWorker'
import { ASSISTANT_BUBBLE_SELECTOR, assistantBubbles } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

const latestHistoryMarker = 'GENERATED_LATEST_HISTORY_MARKER'

async function expectLatestHistory(page: Page, scope?: Locator): Promise<void> {
  const visible = assistantBubbles(page)
  const bubbles = scope ? visible.and(scope.locator(ASSISTANT_BUBBLE_SELECTOR)) : visible
  const latest = bubbles.filter({ hasText: latestHistoryMarker })
  // Hidden premeasurement rows keep identical text and IDs. Persistent
  // duplicate visible rows must still fail this exact-count assertion.
  await expect(latest).toHaveCount(1)
  await expect(latest).toBeVisible()
}

/** Read only generated fixture DOM; retain transient measurement evidence. */
async function installMarkerDiagnostics(page: Page): Promise<void> {
  await page.addInitScript((marker) => {
    const samples: unknown[] = []
    Reflect.set(window, '__nativeMarkerDomSamples', samples)
    let previous = ''
    const capture = () => {
      const copies = Array.from(document.querySelectorAll('[data-testid="native-agent-transcript"] p'))
        .filter(element => element.textContent === marker)
        .map((element) => {
          const ancestors = []
          for (let ancestor: Element | null = element; ancestor && ancestor !== document.body; ancestor = ancestor.parentElement) {
            const style = getComputedStyle(ancestor)
            ancestors.push({
              tag: ancestor.tagName,
              className: ancestor.getAttribute('class'),
              inlineStyle: ancestor.getAttribute('style'),
              hidden: ancestor.hasAttribute('hidden'),
              ariaHidden: ancestor.getAttribute('aria-hidden'),
              seq: ancestor.getAttribute('data-seq'),
              testId: ancestor.getAttribute('data-testid'),
              chatInstanceId: ancestor.getAttribute('data-chat-instance-id'),
              display: style.display,
              visibility: style.visibility,
              opacity: style.opacity,
              rectangles: ancestor.getClientRects().length,
            })
          }
          return { ancestors }
        })
      const signature = JSON.stringify(copies)
      if (signature !== previous) {
        previous = signature
        samples.push({ url: location.href, at: performance.now(), copies })
        if (samples.length > 100)
          samples.shift()
      }
    }
    new MutationObserver(capture).observe(document, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: ['class', 'style', 'hidden', 'aria-hidden'],
    })
  }, latestHistoryMarker)
}

for (const provider of ['codex', 'claude'] as const) {
  test(`links saved ${provider} chat across computers, restores it and unlinks without touching the source`, async ({ page, leapmuxServer }, testInfo) => {
    const fixture = nativeConversationFixture(provider)
    const title = `Generated linked ${provider} workspace`
    const repo = createGitRepo(leapmuxServer.dataDir, `linked-${provider}-checkout`)
    const workspace = await showWorkspaceWithAgents(page, leapmuxServer, title, { workingDir: repo })
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    if (provider === 'claude')
      await installMarkerDiagnostics(page)
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
        await expectLatestHistory(page, viewer)
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
      await expectLatestHistory(page)
      await expect(page.getByText(`Generated source ${provider} computer`, { exact: false }).first()).toBeVisible()
      expect(fixture.unchanged()).toBe(true)
      await page.reload()
      await expect(link).toHaveCount(1)
      await link.click()
      await expect(link).toHaveAttribute('data-tab-id', tabId!)
      await expectLatestHistory(page)
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
      try {
        await expectLatestHistory(page, page.getByTestId('native-agent-transcript'))
      }
      finally {
        if (provider === 'claude') {
          const samples = await page.evaluate(() => Reflect.get(window, '__nativeMarkerDomSamples') as unknown)
          await testInfo.attach('generated-native-marker-dom', { body: JSON.stringify(samples, null, 2), contentType: 'application/json' })
        }
      }
      expect(errors).toEqual([])
      expect(workspace.workspaceId).toBeTruthy()
    })
  })
}
