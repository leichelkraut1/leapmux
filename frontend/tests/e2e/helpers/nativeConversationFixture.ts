import { createHash, randomUUID } from 'node:crypto'
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { createTestDirectory } from './runDirectory'
import { writeToolImage } from './toolImages'

export type NativeConversationFixtureProvider = 'codex' | 'claude'

/** Only generated harmless history, under the launcher's private run directory. */
export function nativeConversationFixture(provider: NativeConversationFixtureProvider) {
  const home = createTestDirectory(`readonly-${provider}-`)
  const cwd = join(home, 'project')
  const codexHome = join(home, '.codex')
  const claudeHome = join(home, '.claude')
  for (const directory of [cwd, codexHome, claudeHome])
    mkdirSync(directory, { mode: 0o700 })
  const sessionId = randomUUID()
  const image = readFileSync(join(cwd, writeToolImage(cwd, 'readonly-fixture'))).toString('base64')
  let parentUuid: string | null = null
  const claude = (role: 'user' | 'assistant', content: unknown[]) => {
    const uuid = randomUUID()
    const row = { type: role, sessionId, cwd, uuid, parentUuid, isSidechain: false, timestamp: new Date().toISOString(), message: { role, content } }
    parentUuid = uuid
    return row
  }
  const codex = (role: 'user' | 'assistant', text: string) => ({
    timestamp: new Date().toISOString(),
    type: 'response_item',
    payload: { type: 'message', role, content: [{ type: role === 'user' ? 'input_text' : 'output_text', text }] },
  })
  const rows: unknown[] = provider === 'codex'
    ? [{ timestamp: new Date().toISOString(), type: 'session_meta', payload: { id: sessionId, cwd } }]
    : []
  for (let index = 0; index < 230; index++) {
    const text = index === 0 ? 'GENERATED_OLDEST_HISTORY_MARKER' : `Generated harmless historical message ${index}.`
    rows.push(provider === 'codex' ? codex(index % 2 === 0 ? 'user' : 'assistant', text) : claude(index % 2 === 0 ? 'user' : 'assistant', [{ type: 'text', text }]))
  }
  if (provider === 'claude') {
    rows.push(claude('assistant', [
      { type: 'thinking', thinking: 'Inspect the harmless generated output.' },
      { type: 'text', text: 'GENERATED_MIXED_BLOCK_TEXT' },
      { type: 'tool_use', id: 'generated-call', name: 'Bash', input: { command: 'printf GENERATED_TOOL_RESULT' } },
    ]))
    rows.push(claude('user', [{ type: 'tool_result', tool_use_id: 'generated-call', content: 'GENERATED_TOOL_RESULT', is_error: false }]))
    rows.push(claude('user', [{ type: 'text', text: 'Generated inline image.' }, { type: 'image', source: { type: 'base64', media_type: 'image/png', data: image } }]))
  }
  else {
    rows.push({ type: 'response_item', payload: { type: 'function_call', name: 'exec_command', call_id: 'generated-call', arguments: JSON.stringify({ cmd: 'printf GENERATED_TOOL_RESULT' }) } })
    rows.push({ type: 'response_item', payload: { type: 'function_call_output', call_id: 'generated-call', output: 'GENERATED_TOOL_RESULT' } })
    rows.push({ type: 'response_item', payload: { type: 'message', role: 'user', content: [
      { type: 'input_text', text: 'Generated inline image.' },
      { type: 'input_image', image_url: `data:image/png;base64,${image}` },
    ] } })
  }
  rows.push(provider === 'codex' ? codex('assistant', 'GENERATED_LATEST_HISTORY_MARKER') : claude('assistant', [{ type: 'text', text: 'GENERATED_LATEST_HISTORY_MARKER' }]))
  const directory = provider === 'codex' ? join(codexHome, 'sessions', '2026', '10', '28') : join(claudeHome, 'projects', cwd.replace(/[^a-z0-9]/gi, '-'))
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  const file = join(directory, provider === 'codex' ? `rollout-2026-10-28T00-00-00-${sessionId}.jsonl` : `${sessionId}.jsonl`)
  writeFileSync(file, `${rows.map(row => JSON.stringify(row)).join('\n')}\n`, { mode: 0o600 })
  const index = join(codexHome, 'state_5.sqlite')
  if (provider === 'codex') {
    const database = new DatabaseSync(index)
    try {
      database.exec('CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, cwd TEXT, name TEXT, title TEXT, archived INTEGER, updated_at_ms INTEGER, recency_at_ms INTEGER)')
      database.prepare('INSERT INTO threads VALUES (?, ?, ?, ?, ?, 0, 1, 1)').run(sessionId, file, cwd, 'Generated Codex read-only chat', 'Generated Codex read-only chat')
    }
    finally {
      database.close()
    }
  }
  const hash = (path: string) => createHash('sha256').update(readFileSync(path)).digest('hex')
  const initialFileHash = hash(file)
  const initialIndexHash = provider === 'codex' ? hash(index) : undefined
  return {
    home,
    sessionId,
    file,
    env: { HOME: home, USERPROFILE: home, CODEX_HOME: codexHome, CODEX_SQLITE_HOME: codexHome, CLAUDE_CONFIG_DIR: claudeHome },
    unchanged: () => hash(file) === initialFileHash && (initialIndexHash === undefined || hash(index) === initialIndexHash),
    append: (text: string) => {
      const row = provider === 'codex' ? codex('assistant', text) : claude('assistant', [{ type: 'text', text }])
      appendFileSync(file, `${JSON.stringify(row)}\n`)
    },
    hash: () => hash(file),
  }
}
