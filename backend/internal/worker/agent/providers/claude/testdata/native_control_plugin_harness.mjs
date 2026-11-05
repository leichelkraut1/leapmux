import assert from 'node:assert/strict'
import { randomUUID, webcrypto } from 'node:crypto'
import { mkdtemp, writeFile, unlink, rmdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { setImmediate } from 'node:timers/promises'

globalThis.crypto ??= webcrypto
const turn = 'generated-current-turn'
const session = 'generated-session'
const flush = async () => { for (let i = 0; i < 8; i++) await setImmediate() }
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }

async function fixture(source) {
  const handlers = new Map()
  const state = { posts: [], activeTurn: '', approvals: 0, rejected: 0, fallbacks: 0, timer: null, onPoll: null }
  const descriptor = { url: 'http://127.0.0.1:54321', token: 'A'.repeat(43), epoch: 'B'.repeat(43), workerPid: 999, workerStart: 1 }
  const response = (value, ok = true) => ({ ok, text: JSON.stringify(value) })
  const $ = {
    env: { get: async () => undefined }, session: { id: async () => session, version: async () => ({ version: '2.1.308' }) },
    fs: { read: async () => JSON.stringify(descriptor) },
    clock: { now: async () => 1000, every: (_, callback) => { state.timer = callback } },
    prompt: { submit: async () => ({}) }, turn: { abort: async () => {} },
    http: { fetch: async (url, init) => {
      const path = new URL(url).pathname
      const body = JSON.parse(init.body)
      state.posts.push({ path, body })
      if (path === '/register') {
        state.activeTurn = body.activeTurn
        return response({ epoch: descriptor.epoch })
      }
      if (path === '/poll') {
        state.activeTurn = body.activeTurn
        if (state.onPoll) {
          const custom = await state.onPoll(body)
          if (custom) return custom
        }
        return response({ epoch: descriptor.epoch, commands: [] })
      }
      assert.equal(path, '/approval')
      if (body.activeTurn !== state.activeTurn) {
        state.rejected++
        return response({ error: 'stale' }, false)
      }
      state.approvals++
      return response({ epoch: descriptor.epoch, command: {
        kind: 'approval', instanceId: body.instanceId, turnId: body.activeTurn,
        requestId: 'generated-answer', deliveryId: 'C'.repeat(43), promptId: 'D'.repeat(43), optionId: 'allow_once',
      } })
    } },
  }
  const substitutions = {
    __LEAPMUX_DESCRIPTOR_JSON__: JSON.stringify('/generated/public-fixture-descriptor.json'),
    __LEAPMUX_POLL_MS__: '5000', __LEAPMUX_RETENTION_MS__: '60000',
    __LEAPMUX_APPROVAL_TEXT_BYTES__: '1024', __LEAPMUX_APPROVAL_DETAIL_BYTES__: '65536', __LEAPMUX_MAX_APPROVALS__: '16',
  }
  for (const [key, value] of Object.entries(substitutions)) source = source.replaceAll(key, value)
  const generatedDirectory = await mkdtemp(join(tmpdir(), 'leapmux-approval-regression-'))
  const generatedModule = join(generatedDirectory, 'plugin-' + randomUUID() + '.mjs')
  await writeFile(generatedModule, source, { flag: 'wx', mode: 0o600 })
  const module = await import(pathToFileURL(generatedModule).href)
  await unlink(generatedModule)
  await rmdir(generatedDirectory)
  module.register((name, callback) => { handlers.set(name, callback); return { catch() {} } })
  await handlers.get('session.start')($, {}, async () => undefined)
  await flush()
  assert.equal(typeof state.timer, 'function')
  assert.equal(state.posts.filter(p => p.path === '/poll').length, 1)
  const start = () => handlers.get('turn.start')($, { turnId: turn }, async () => undefined)
  const event = { session_id: session, tool_name: 'Bash', tool_input: { command: 'printf harmless' }, cwd: '/generated/private-fixture' }
  const ask = (signal = new AbortController().signal, changed = {}) => {
    const actual = { ...event, ...changed }
    const next = Object.assign(async value => { assert.equal(value, actual); state.fallbacks++; return { nativeFallback: true } }, { signal })
    return handlers.get('classic.PermissionRequest')($, actual, next)
  }
  return { $, state, descriptor, handlers, start, ask, response }
}

export async function probeImmediateTurn(source) {
  const f = await fixture(source)
  await f.start()
  const result = await f.ask()
  await flush()
  return { decision: result?.decision?.behavior ?? null, nativeFallback: f.state.fallbacks,
    acceptedApprovals: f.state.approvals, staleRejections: f.state.rejected,
    requestOrder: f.state.posts.map(p => ({ path: p.path, activeTurn: p.body.activeTurn })) }
}

export async function runRegressions(source) {
  const passed = []
  {
    const result = await probeImmediateTurn(source)
    assert.equal(result.decision, 'allow')
    assert.equal(result.acceptedApprovals, 1)
    assert.equal(result.staleRejections, 0)
    const approvalIndex = result.requestOrder.findIndex(p => p.path === '/approval')
    assert.equal(result.requestOrder[approvalIndex - 1].path, '/poll')
    assert.equal(result.requestOrder[approvalIndex - 1].activeTurn, turn)
    passed.push('new turn before its first timer heartbeat is acknowledged before approval')
  }
  {
    const f = await fixture(source)
    const gate = deferred()
    f.state.onPoll = async body => { if (body.activeTurn === '') await gate.promise }
    const oldPoll = f.state.timer()
    await flush()
    await f.start()
    const pending = f.ask()
    await flush()
    assert.equal(f.state.posts.filter(p => p.path === '/approval').length, 0)
    gate.resolve()
    await oldPoll
    assert.equal((await pending)?.decision?.behavior, 'allow')
    const posts = f.state.posts
    const index = posts.findIndex(p => p.path === '/approval')
    assert.equal(posts[index - 1].body.activeTurn, turn)
    await flush()
    await f.state.timer()
    assert.equal(f.state.posts.flatMap(p => p.body.outcomes ?? []).filter(o => o.state === 'answered').length, 1)
    passed.push('an older in-flight poll is awaited, then a fresh turn poll; answer outcome sent once')
  }
  {
    const f = await fixture(source)
    await f.start()
    f.state.onPoll = async () => f.response({ error: 'generated unavailable' }, false)
    assert.equal((await f.ask()).nativeFallback, true)
    assert.equal(f.state.posts.filter(p => p.path === '/approval').length, 0)
    passed.push('failed heartbeat returns native flow without posting an approval or replay')
  }
  {
    const f = await fixture(source)
    await f.start()
    const controller = new AbortController()
    f.state.onPoll = async () => { controller.abort(); return undefined }
    assert.equal((await f.ask(controller.signal)).nativeFallback, true)
    assert.equal(f.state.posts.filter(p => p.path === '/approval').length, 0)
    passed.push('native abort during heartbeat prevents the approval post')
  }
  {
    const f = await fixture(source)
    await f.start()
    f.state.onPoll = async () => {
      await f.handlers.get('turn.complete')(f.$, { turnId: turn, isAborted: false }, async () => undefined)
    }
    assert.equal((await f.ask()).nativeFallback, true)
    assert.equal(f.state.posts.filter(p => p.path === '/approval').length, 0)
    passed.push('current turn completion during heartbeat prevents a stale approval post')
  }
  {
    const f = await fixture(source)
    await f.start()
    f.descriptor.epoch = 'E'.repeat(43)
    assert.equal((await f.ask()).nativeFallback, true)
    assert.equal(f.state.posts.filter(p => p.path === '/approval').length, 0)
    passed.push('changed Worker epoch is not replayed into a new bridge')
  }
  {
    const f = await fixture(source)
    await f.start()
    const before = f.state.posts.length
    assert.equal((await f.ask(undefined, { session_id: 'other-session' })).nativeFallback, true)
    assert.equal(f.state.posts.length, before)
    passed.push('another native session is rejected before synchronization')
  }
  return { passed, count: passed.length, actualNetworkCalls: 0, actualProviderOrModelCalls: 0,
    generatedDescriptorOnly: true, actualFileEffects: false }
}
