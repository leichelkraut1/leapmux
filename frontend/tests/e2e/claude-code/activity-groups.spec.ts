import { claudeTest } from '../claude-fixtures'
import { exerciseActivityGroups } from '../helpers/nativeActivityGroups'

claudeTest('folds native tool activity while keeping the final reply visible', async ({ native }, testInfo) => {
  await exerciseActivityGroups(native, testInfo)
})
