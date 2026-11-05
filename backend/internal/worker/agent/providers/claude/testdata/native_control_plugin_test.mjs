import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { runRegressions } from './native_control_plugin_harness.mjs'

test('native permission approval waits for an acknowledged current turn', async t => {
  const source = await readFile(new URL('../native-control-plugin/hooks/register.js', import.meta.url), 'utf8')
  const result = await runRegressions(source)
  assert.equal(result.count, 7)
  for (const name of result.passed) t.diagnostic(name)
})
