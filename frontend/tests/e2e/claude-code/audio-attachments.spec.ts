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

attachmentTest('makes audio available as an exact Worker file for tools after reload', async ({ native }) => {
  await exerciseWorkerFileAttachment(native, { kind: 'audio', fileName: 'claude-code-audio.wav' })
})
