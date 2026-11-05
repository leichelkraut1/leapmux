import { join } from 'node:path'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { fileTabPayload } from '~/lib/tabPayload'
import { test } from './fixtures'
import { createWorkspaceViaAPI, getTestChannel } from './helpers/api'
import { expandWorkspaceRow, openWorkspace, sidebarLeafIds, sidebarLeaves, treeRow, waitForLayoutSave, waitForWorkspaceReady, workspaceRow } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { moveSidebarTabToWorkspace } from './helpers/workspaceMoveMenu'
import { commitFile, createGitRepo } from './helpers/worktree'

/**
 * File-tab path bookkeeping over the E2EE channel.
 *
 * File tabs are unique because the path lives only on the worker behind the
 * E2EE channel; the hub never sees it. The worker keys that row by
 * `(user_id, tab_id)` and nothing else -- there is no workspace column and no
 * Relocate RPC, because a cross-workspace move is now a pure CRDT tile write
 * with no worker leg at all. That is what makes the move work with the worker
 * offline; it is also why this spec asserts the row survives such a move
 * untouched rather than following it.
 *
 * The CRDT-side move-as-a-tile_id-write is exercised in the backend
 * cross_workspace_move_test.go integration test and in
 * frontend/src/components/shell/crossWorkspaceMove.test.ts.
 */

test.describe('file-tab E2EE worker round-trip', () => {
  test('a file moves through the sidebar menu without changing its worker payload and survives reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const repo = createGitRepo(leapmuxServer.dataDir, 'file-menu-move')
    commitFile(repo, 'move-note.txt', 'Harmless file-move fixture.\n', 'add file fixture')
    const { workspaceId: source } = await showWorkspaceWithAgents(page, leapmuxServer, 'File menu source', { workingDir: repo })
    await treeRow(page, 'move-note.txt').click()
    const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]:visible').first()
    await expect(fileTab).toBeVisible()
    const tabId = await fileTab.getAttribute('data-tab-id')
    if (!tabId)
      throw new Error('The opened file tab has no ID.')
    const target = await createWorkspaceViaAPI(hubUrl, adminToken, 'File menu target')
    await expect(workspaceRow(page, target)).toBeVisible()
    const channel = await getTestChannel(hubUrl, adminToken)
    const { GetTabPayloadRequestSchema, GetTabPayloadResponseSchema } = await import('../../src/generated/proto/leapmux/v1/worker_private_pb')
    const readPayload = () => channel.callWorker(workerId, 'GetTabPayload', GetTabPayloadRequestSchema, GetTabPayloadResponseSchema, { tabId })
    const before = await readPayload()
    expect(before.payload?.kind.case === 'file' && before.payload.kind.value.filePath).toBe(join(repo, 'move-note.txt'))
    await expandWorkspaceRow(page, source)
    const fileLeaf = sidebarLeaves(page, source).and(page.locator(`[data-tab-id="${tabId}"]`))
    const saved = waitForLayoutSave(page)
    await moveSidebarTabToWorkspace(page, fileLeaf, target)
    await saved
    await expandWorkspaceRow(page, target)
    await expect.poll(() => sidebarLeafIds(page, target)).toContain(tabId)
    await expect.poll(() => sidebarLeafIds(page, source)).not.toContain(tabId)
    expect((await readPayload()).payload).toEqual(before.payload)

    await openWorkspace(page, target)
    await expect(fileTab).toHaveAttribute('data-tab-id', tabId)
    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(fileTab).toHaveAttribute('data-tab-id', tabId)
    await expect.poll(() => sidebarLeafIds(page, target)).toContain(tabId)
    await expandWorkspaceRow(page, source)
    await expect.poll(() => sidebarLeafIds(page, source)).not.toContain(tabId)
    expect((await readPayload()).payload).toEqual(before.payload)
  })

  test('register / get / revoke round-trip is keyed by tab id alone', async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    // No workspace: the worker keys the row by the tab ID alone, and a channel
    // carries no workspace set.
    const channel = await getTestChannel(hubUrl, adminToken)

    const {
      RegisterTabPayloadRequestSchema,
      RegisterTabPayloadResponseSchema,
      GetTabPayloadRequestSchema,
      GetTabPayloadResponseSchema,
      RevokeTabPayloadRequestSchema,
      RevokeTabPayloadResponseSchema,
    } = await import('../../src/generated/proto/leapmux/v1/worker_private_pb')

    const tabId = `t-${Date.now()}`
    const filePath = '/repo/test-file.go'

    // 1. Register. No workspace is named -- the worker has nowhere to put one.
    await channel.callWorker(
      workerId,
      'RegisterTabPayload',
      RegisterTabPayloadRequestSchema,
      RegisterTabPayloadResponseSchema,
      { tabId, payload: fileTabPayload(filePath, '') },
    )

    // 2. Get returns the path.
    const got = await channel.callWorker(
      workerId,
      'GetTabPayload',
      GetTabPayloadRequestSchema,
      GetTabPayloadResponseSchema,
      { tabId },
    )
    expect(got.payload?.kind.case === 'file' && got.payload.kind.value.filePath).toBe(filePath)

    // 3. Revoke removes the row, so a later Get is refused as NOT_FOUND. The
    //    check requires that refusal, so another failure, such as a channel
    //    fault, cannot pass for a revoked row.
    await channel.callWorker(
      workerId,
      'RevokeTabPayload',
      RevokeTabPayloadRequestSchema,
      RevokeTabPayloadResponseSchema,
      { tabId },
    )
    const afterRevoke = await channel.callWorker(
      workerId,
      'GetTabPayload',
      GetTabPayloadRequestSchema,
      GetTabPayloadResponseSchema,
      { tabId },
    ).then(() => null, (error: unknown) => error)
    expect(afterRevoke, 'the Get after the revoke is refused as not found')
      .toMatchObject({ name: 'ChannelError', source: 'rpc', code: Code.NotFound })
  })
})
