import { codexTest } from '../codex-fixtures'
import { exerciseActivityGroups } from '../helpers/nativeActivityGroups'
import { codexExecContext } from './scenarios'

codexTest('folds native tool activity while keeping the final reply visible', async ({ native }, testInfo) => {
  await exerciseActivityGroups(codexExecContext(native), testInfo)
})
