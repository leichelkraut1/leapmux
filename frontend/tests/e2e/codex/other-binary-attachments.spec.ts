import { codexTest } from '../codex-fixtures'
import { exerciseWorkerFileAttachment } from '../helpers/workerFileAttachment'

codexTest('makes a binary attachment available as an exact worker file for tools', async ({ native }) => {
  await exerciseWorkerFileAttachment(native, { kind: 'binary', fileName: 'codex-project.ACD' })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
