import type { Locator, Page } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { fromJsonString } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { WorkerPlacementStrategy, WorkflowTemplateBundleSchema } from '../../src/generated/proto/leapmux/v1/workflow_pb'
import { test } from './fixtures'
import { appMenuTrigger } from './helpers/ui'

/**
 * Rendered library round-trips through the suite's real encrypted Worker channel.
 * An empty workspace avoids starting any provider; Launch is never submitted.
 * Each imported/profile name is unique because libraries outlive a workspace.
 */
async function openWorkflows(page: Page): Promise<Locator> {
  await appMenuTrigger(page).click()
  await page.getByRole('menuitem', { name: 'Workflows', exact: true }).click()
  const dialog = page.getByTestId('workflows-dialog')
  await expect(dialog).toBeVisible()
  await expect(page.locator('vite-error-overlay')).toHaveCount(0)
  return dialog
}

async function librarySection(page: Page, dialog: Locator, section: 'Profiles' | 'Templates', workerId: string) {
  await dialog.getByRole('radio', { name: section, exact: true }).click()
  await dialog.getByTestId('workflow-library-computer').click()
  await page.getByTestId(`workflow-computer-option-${workerId}`).click()
  await expect(dialog.getByTestId('workflow-library-computer')).toContainText('Local')
}

function profileRow(dialog: Locator, name: string): Locator {
  return dialog.getByTestId('workflow-profile-row').filter({ has: dialog.page().getByRole('heading', { name, exact: true }) })
}

function templateRow(dialog: Locator, name: string): Locator {
  return dialog.getByTestId('workflow-template-row').filter({ has: dialog.page().getByRole('heading', { name, exact: true }) })
}

async function deleteLibraryRow(row: Locator) {
  await row.getByRole('button', { name: 'Delete', exact: true }).click()
  await row.getByRole('button', { name: 'Delete?', exact: true }).click()
  await expect(row).toHaveCount(0)
}

function testBundle(prefix: string, workerId: string) {
  return {
    format_version: 1,
    profiles: ['lead', 'builder'].map(key => ({
      id: key,
      name: `${prefix} ${key}`,
      agent_provider: 'AGENT_PROVIDER_CLAUDE_CODE',
      placement: { strategy: 'WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER', worker_id: workerId },
    })),
    template: {
      name: `${prefix} template`,
      roles: [
        { key: 'lead', name: 'Lead', profile_id: 'lead', is_lead: true },
        { key: 'builder', name: 'Builder', profile_id: 'builder', required_outputs: ['summary'], review_required: true },
      ],
      steps: [{ key: 'implement', title: 'Implement', role_key: 'builder', instructions: 'Generated UI fixture; do not launch.' }],
      max_parallel_tasks: 1,
      max_agents: 2,
      confine_agents: false,
    },
  }
}

async function importTemplate(dialog: Locator, prefix: string, workerId: string) {
  await dialog.getByRole('button', { name: 'Import...', exact: true }).click()
  const importing = dialog.getByTestId('workflow-import')
  await importing.getByLabel('Workflow document (YAML or JSON)', { exact: true }).fill(JSON.stringify(testBundle(prefix, workerId)))
  await importing.getByRole('button', { name: 'Import', exact: true }).click()
  await expect(importing).toHaveCount(0)
  await expect(templateRow(dialog, `${prefix} template`)).toBeVisible()
}

test.describe('Workflows library', () => {
  test('creates, edits, reloads and deletes a profile pinned to the actual computer', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }, testInfo) => {
    void authenticatedEmptyWorkspace
    const name = `Profile QA ${crypto.randomUUID().slice(0, 8)}`
    const updatedName = `${name} edited`
    let dialog = await openWorkflows(page)
    await librarySection(page, dialog, 'Profiles', leapmuxServer.workerId)
    await dialog.getByRole('button', { name: 'New profile', exact: true }).click()
    const editor = dialog.getByTestId('workflow-profile-editor')
    await editor.getByLabel('Name', { exact: true }).fill(name)
    await editor.getByTestId('agent-provider-selector-trigger').click()
    await page.getByTestId(`agent-provider-option-${AgentProvider.CLAUDE_CODE}`).click()
    await editor.getByRole('radio', { name: 'Specific computer', exact: true }).click()
    await editor.getByTestId('profile-placement-computer').click()
    await page.getByTestId(`workflow-computer-option-${leapmuxServer.workerId}`).click()
    await editor.getByRole('button', { name: 'Save profile', exact: true }).click()
    const saved = profileRow(dialog, name)
    await expect(saved).toContainText('Local')
    await saved.getByRole('button', { name: 'Edit', exact: true }).click()
    await editor.getByLabel('Name', { exact: true }).fill(updatedName)
    await editor.getByRole('button', { name: 'Save profile', exact: true }).click()
    await expect(profileRow(dialog, updatedName)).toBeVisible()

    // Navigation reconstructs the component; only a Worker-backed save can survive it.
    await page.reload()
    dialog = page.getByTestId('workflows-dialog')
    await expect(profileRow(dialog, updatedName)).toContainText('Local')
    await dialog.screenshot({ path: testInfo.outputPath('workflows-profile-desktop.png') })
    await deleteLibraryRow(profileRow(dialog, updatedName))
  })

  test('imports a template, saves a role placement, exports it and deletes independent copies', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }, testInfo) => {
    void authenticatedEmptyWorkspace
    const prefix = `Template QA ${crypto.randomUUID().slice(0, 8)}`
    const editedName = `${prefix} edited`
    const dialog = await openWorkflows(page)
    await librarySection(page, dialog, 'Templates', leapmuxServer.workerId)
    await importTemplate(dialog, prefix, leapmuxServer.workerId)
    await templateRow(dialog, `${prefix} template`).getByRole('button', { name: 'Edit', exact: true }).click()
    const editor = dialog.getByTestId('workflow-template-editor')
    await expect(editor.getByLabel('Restrict agents to this workflow', { exact: false })).not.toBeChecked()
    await editor.getByLabel('Name', { exact: true }).first().fill(editedName)
    const builder = editor.getByTestId('workflow-role-editor').nth(1)
    await expect(builder.getByLabel('Key', { exact: true })).toHaveValue('builder')
    await builder.getByText('Overrides', { exact: true }).click()
    await builder.getByLabel('Override the profile\'s placement', { exact: true }).check()
    await builder.getByRole('radio', { name: 'Specific computer', exact: true }).click()
    await builder.getByTestId('role-1-placement-computer').click()
    await page.getByTestId(`workflow-computer-option-${leapmuxServer.workerId}`).click()
    await editor.getByRole('button', { name: 'Save template', exact: true }).click()
    await expect(templateRow(dialog, editedName)).toBeVisible()

    const downloaded = page.waitForEvent('download')
    await templateRow(dialog, editedName).getByRole('button', { name: 'Export JSON', exact: true }).click()
    const path = await (await downloaded).path()
    if (!path)
      throw new Error('The exported template download has no local file.')
    const exported = fromJsonString(WorkflowTemplateBundleSchema, await readFile(path, 'utf8'))
    expect(exported.template?.name).toBe(editedName)
    expect(exported.template?.confineAgents).toBe(false)
    const exportedBuilder = exported.template?.roles.find(role => role.key === 'builder')
    expect(exportedBuilder?.agent?.placement?.strategy).toBe(WorkerPlacementStrategy.SPECIFIC_WORKER)
    expect(exportedBuilder?.agent?.placement?.workerId).toBe(leapmuxServer.workerId)
    expect(exported.profiles).toHaveLength(2)

    await templateRow(dialog, editedName).getByRole('button', { name: 'Duplicate', exact: true }).click()
    const copies = dialog.getByTestId('workflow-template-row').filter({ hasText: prefix })
    await expect(copies).toHaveCount(2)
    await dialog.screenshot({ path: testInfo.outputPath('workflows-template-desktop.png') })
    await deleteLibraryRow(templateRow(dialog, editedName))
    await expect(copies).toHaveCount(1)
    await deleteLibraryRow(copies.first())
    await librarySection(page, dialog, 'Profiles', leapmuxServer.workerId)
    await deleteLibraryRow(profileRow(dialog, `${prefix} lead`))
    await deleteLibraryRow(profileRow(dialog, `${prefix} builder`))
  })
})

test.describe('Workflows on a narrow screen', () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })

  test('keeps per-role placement and launch validation usable without starting an agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }, testInfo) => {
    void authenticatedEmptyWorkspace
    const prefix = `Mobile QA ${crypto.randomUUID().slice(0, 8)}`
    const dialog = await openWorkflows(page)
    await librarySection(page, dialog, 'Templates', leapmuxServer.workerId)
    await importTemplate(dialog, prefix, leapmuxServer.workerId)
    await templateRow(dialog, `${prefix} template`).getByRole('button', { name: 'Launch', exact: true }).click()
    const launch = dialog.getByTestId('workflow-launch-form')
    const launchButton = launch.getByRole('button', { name: 'Launch', exact: true })
    await expect(launch.getByTestId('workflow-launch-refusal')).toHaveText('Describe the objective.')
    await expect(launchButton).toBeDisabled()
    await expect(launch.getByText('The Lead is pinned to', { exact: true })).toBeVisible()
    const builderRow = launch.locator('[data-testid="workflow-launch-role"][data-role-key="builder"]')
    await builderRow.getByTestId('workflow-launch-role-computer-builder').click()
    await page.getByTestId(`workflow-computer-option-${leapmuxServer.workerId}`).click()
    await expect(builderRow.getByTestId('workflow-launch-role-computer-builder')).toContainText('Local')
    await launch.getByTestId('workflow-launch-objective').fill('Generated layout check only; do not launch agents.')
    await expect(launch.getByTestId('workflow-launch-project-dir')).not.toHaveValue('')
    await expect(launchButton).toBeEnabled()
    const box = await dialog.boundingBox()
    if (!box)
      throw new Error('The workflow dialog has no rendered bounds.')
    expect(box.x).toBeGreaterThanOrEqual(-1)
    expect(box.x + box.width).toBeLessThanOrEqual(391)
    await launch.getByTestId('workflow-launch-objective').scrollIntoViewIfNeeded()
    await dialog.screenshot({ path: testInfo.outputPath('workflows-launch-mobile.png') })
    await launch.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(launch).toHaveCount(0)
    await deleteLibraryRow(templateRow(dialog, `${prefix} template`))
    await librarySection(page, dialog, 'Profiles', leapmuxServer.workerId)
    await deleteLibraryRow(profileRow(dialog, `${prefix} lead`))
    await deleteLibraryRow(profileRow(dialog, `${prefix} builder`))
  })
})
