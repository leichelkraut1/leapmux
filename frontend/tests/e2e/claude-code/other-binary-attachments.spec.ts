import { claudeTest } from '../claude-fixtures'
import { exerciseWorkerFileAttachment } from '../helpers/workerFileAttachment'
import { authenticatedAgentWorkspace } from '../helpers/workspace'
import { CLAUDE_AGENT } from './scenarios'

const attachmentTest = claudeTest.extend({
  providerWorkspace: authenticatedAgentWorkspace({
    ...CLAUDE_AGENT,
    openOptions: { optionValues: { permissionMode: 'default' } },
  }),
})

attachmentTest.describe('Attachment Support', () => {
  // The Linux/WebKitGTK image-paste path (entirely empty DataTransfer →
  // OS clipboard read via the Tauri clipboard-manager plugin) cannot be
  // exercised in headless Chromium: the bug is that WebKitGTK does not
  // populate DataTransfer at all, and Chromium does not reproduce that
  // shape. The conversion logic is covered by the platformBridge unit
  // test; manual paste in the desktop build is the only true end-to-end.

  attachmentTest('makes a binary attachment available as an exact worker file for tools', async ({ native }) => {
    // Binary files are delivered through a readable Worker path, not a native model block.
    await exerciseWorkerFileAttachment(native, { kind: 'binary', fileName: 'claude-project.ACD' })
  })
})
