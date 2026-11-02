import { codexTest } from '../codex-fixtures'
import { exerciseWorkerFileAttachment } from '../helpers/workerFileAttachment'

codexTest('makes a pdf available as an exact worker file without claiming native pdf input', async ({ native }) => {
  await exerciseWorkerFileAttachment(native, { kind: 'pdf', fileName: 'codex-drawing.pdf' })
})
