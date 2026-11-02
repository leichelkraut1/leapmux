import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { nativeConversationFixture } from './helpers/nativeConversationFixture'
import { withNativeWorker } from './helpers/nativeWorker'
import { activityGroupToggles, assistantBubbles, loginViaToken, messageContents } from './helpers/ui'

// CSS-visible overscan rows can be outside the clipped viewport. Use real
// input before clicking them so Playwright's automatic scrollIntoView does not
// masquerade as an unexplained product scroll jump.
async function revealWithWheel(page: Page, scroll: Locator, target: Locator): Promise<void> {
  await expect.poll(async () => {
    const row = await target.boundingBox()
    const viewport = await scroll.boundingBox()
    if (!row || !viewport)
      return false
    const top = viewport.y + 4
    const bottom = viewport.y + viewport.height - 4
    if (row.y >= top && row.y + row.height <= bottom)
      return true
    const delta = row.y < top ? row.y - top - 16 : row.y + row.height - bottom + 16
    await scroll.hover()
    await page.mouse.wheel(0, delta)
    return false
  }, { message: 'the native history target enters the clipped chat viewport through user input' }).toBe(true)
  await expect(target).toBeInViewport()
}

for (const provider of ['codex', 'claude'] as const) {
  for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
    test(`reads saved ${provider} history without taking control at ${viewport.width}px`, async ({ page, leapmuxServer }, testInfo) => {
      const fixture = nativeConversationFixture(provider)
      const errors: string[] = []
      const warnings: string[] = []
      const performanceNotes: string[] = []
      page.on('pageerror', error => errors.push(error.message))
      page.on('console', (message) => {
        if (message.type() === 'error')
          errors.push(message.text())
        if (message.type() === 'warning') {
          const text = message.text()
          // The existing 50 ms scroll timing probe is a performance diagnostic,
          // not a portable browser-test budget. Retain its exact measurements
          // as evidence; unexpected scroll jumps and every other warning fail.
          if (/^\[chatScroll\] slow scroll phase \{phase: refreshViewport, ms: \d+, rows: \d+\}$/.test(text))
            performanceNotes.push(text)
          else
            warnings.push(text)
        }
      })
      await page.setViewportSize(viewport)
      await withNativeWorker(leapmuxServer, {
        workerName: `Read-only ${provider} test computer`,
        dataDirPrefix: 'readonly-history-worker',
        env: fixture.env,
        privateDirectories: [fixture.home],
      }, async ({ workerId }) => {
        await loginViaToken(page, leapmuxServer.adminToken)
        const query = new URLSearchParams({ dashboard: 'agents', externalWorker: workerId, externalProvider: provider, externalSession: fixture.sessionId })
        await page.goto(`/?${query}`)
        const viewer = page.getByTestId('native-agent-transcript')
        await expect(viewer).toBeVisible()
        await expect(page.locator('vite-error-overlay, nextjs-portal')).toHaveCount(0)
        await expect(viewer.getByText(/Read only/)).toBeVisible()
        await expect(assistantBubbles(page).filter({ hasText: 'GENERATED_LATEST_HISTORY_MARKER' })).toBeVisible()
        await expect(viewer.locator('[contenteditable="true"], textarea, [data-testid="composer-editor"]')).toHaveCount(0)
        await expect(viewer.getByRole('button', { name: /^(send|interrupt|stop agent|resume|approve|allow|deny|reply|quote)\b/i })).toHaveCount(0)
        expect(fixture.unchanged(), 'viewing leaves the native history and index unchanged').toBe(true)
        const image = viewer.locator('img[src^="data:image/png;base64,"]:visible').first()
        await expect(image).toBeVisible()
        expect(await image.evaluate(element => (element as HTMLImageElement).complete && (element as HTMLImageElement).naturalWidth > 0)).toBe(true)

        const scroll = viewer.locator('[data-chat-scroll-container="true"]')
        const closed = page.locator('[data-testid="activity-group-toggle"][aria-expanded="false"]:visible')
        const closedCount = await closed.count()
        for (let index = 0; index < closedCount && await closed.count() > 0; index++) {
          await revealWithWheel(page, scroll, closed.first())
          await closed.first().click()
        }
        await expect(messageContents(page).filter({ hasText: 'GENERATED_TOOL_RESULT' }).first()).toBeVisible()
        const group = activityGroupToggles(page).last()
        await revealWithWheel(page, scroll, group)
        await expect(group).toHaveAttribute('aria-expanded', 'true')
        const groupHandle = await group.elementHandle()
        await viewer.getByRole('button', { name: 'Refresh native chat history' }).click()
        await expect(viewer.getByRole('button', { name: 'Refresh native chat history' })).toBeEnabled()
        await expect(group).toHaveAttribute('aria-expanded', 'true')
        expect(await groupHandle!.evaluate(element => element.isConnected), 'refresh retains the expanded rendered group').toBe(true)

        fixture.append('GENERATED_LIVE_APPEND_MARKER')
        const appendedHash = fixture.hash()
        await expect(assistantBubbles(page).filter({ hasText: 'GENERATED_LIVE_APPEND_MARKER' })).toBeVisible()
        expect(fixture.hash(), 'tail refresh never writes native history').toBe(appendedHash)
        await revealWithWheel(page, scroll, assistantBubbles(page).filter({ hasText: 'GENERATED_LIVE_APPEND_MARKER' }))
        await page.screenshot({ path: testInfo.outputPath(`${provider}-${viewport.width}-read-only.png`), fullPage: true })

        // The reader exposes the complete saved history through bounded pages.
        const older = viewer.getByRole('button', { name: 'Load older history', exact: true })
        for (let pageIndex = 0; pageIndex < 5 && await older.count() > 0; pageIndex++) {
          await expect(older).toBeEnabled()
          await older.click()
          await expect(viewer.getByRole('button', { name: 'Refresh native chat history' })).toBeEnabled()
        }
        await expect(viewer.getByRole('button', { name: 'Load older history', exact: true })).toHaveCount(0)
        await scroll.hover()
        await page.mouse.wheel(0, -(await scroll.evaluate(element => element.scrollHeight)))
        await expect(messageContents(page).filter({ hasText: 'GENERATED_OLDEST_HISTORY_MARKER' })).toBeVisible()
        await viewer.getByRole('button', { name: 'Close', exact: true }).click()
        await expect(viewer).toHaveCount(0)
        await expect(page).toHaveURL(/dashboard=agents/)
        expect(new URL(page.url()).searchParams.has('externalSession')).toBe(false)
        await testInfo.attach('native-chat-browser-health', {
          body: JSON.stringify({ provider, viewport, errors, warnings, performanceNotes }, null, 2),
          contentType: 'application/json',
        })
        if (performanceNotes.length)
          testInfo.annotations.push({ type: 'performance', description: performanceNotes.join('; ') })
        expect(errors).toEqual([])
        expect(warnings).toEqual([])
      })
    })
  }
}
