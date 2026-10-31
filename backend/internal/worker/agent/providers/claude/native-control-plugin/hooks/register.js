// Claude Mods API; network work runs only in the timer, never hook chains.
let sessionId = null
let instanceId = null
let version = null
let bridge = null
let registered = false
let polling = false
let activeTurn = null
let stopped = false
const outcomes = []
const seen = new Map()
const remoteAborts = new Set()

function validId(value) {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value)
}

function outcome(command, state, reason, turnId) {
  if (outcomes.length >= 2048) return
  outcomes.push({ requestId: command.requestId, deliveryId: command.deliveryId, state, reason, turnId })
  const record = seen.get(command.deliveryId)
  if (record) record.terminal = true
}

function pruneSeen(now) {
  for (const [id, record] of seen) {
    if (record.ackedAt !== null && now - record.ackedAt >= __LEAPMUX_RETENTION_MS__) seen.delete(id)
  }
}

async function post($, path, body) {
  const response = await $.http.fetch(bridge.url + path, {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + bridge.token, 'Content-Type': 'application/json' },
    body: JSON.stringify({ sessionId, instanceId, ...body }),
  })
  if (!response.ok) throw new Error('local_bridge_unavailable')
  const result = JSON.parse(response.text)
  if (result.epoch !== bridge.epoch) throw new Error('local_bridge_epoch_changed')
  return result
}

function safeDescriptor(value) {
  if (!value || typeof value.url !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(value.token) ||
      !/^[A-Za-z0-9_-]{43}$/.test(value.epoch)) return false
  const url = new URL(value.url)
  return url.protocol === 'http:' && url.hostname === '127.0.0.1' &&
    /^[0-9]+$/.test(url.port) && url.pathname === '/' && !url.username && !url.password &&
    !url.search && !url.hash && Number.isSafeInteger(value.workerPid) && value.workerPid > 0 &&
    Number.isSafeInteger(value.workerStart) && value.workerStart > 0
}

async function deliver($, command) {
  if (!validId(command.requestId) || !validId(command.deliveryId) || command.deliveryId.length !== 43 ||
      command.instanceId !== instanceId || seen.has(command.deliveryId)) return
  if (seen.size >= 2048) { outcome(command, 'rejected', 'capacity'); return }
  seen.set(command.deliveryId, { terminal: false, ackedAt: null })
  if (command.kind === 'send' && typeof command.text === 'string') {
    // Never await this promise in a hook or timer: it may wait for a long turn.
    $.prompt.submit({ text: command.text }).then(result => {
      if (result?.drop) {
        const localStop = result.drop.includes('the session was stopped and nobody has sent input since')
        if (localStop) stopped = true
        outcome(command, 'dropped', localStop ? 'local_stop' : 'engine_refused')
      } else {
        outcome(command, 'started', undefined, activeTurn ?? undefined)
      }
    }, () => { outcome(command, 'rejected', 'engine_refused') })
  } else if (command.kind === 'interrupt') {
    if (!activeTurn || command.turnId !== activeTurn) { outcome(command, 'stale_turn'); return }
    remoteAborts.add(command.turnId)
    try {
      await $.turn.abort({ turnId: command.turnId })
      outcome(command, 'interrupted', undefined, command.turnId)
    } catch {
      remoteAborts.delete(command.turnId)
      outcome(command, 'rejected', 'turn_changed')
    }
  } else {
    outcome(command, 'rejected', 'invalid_command')
  }
}

async function tick($) {
  if (polling || !sessionId) return
  polling = true
  try {
    pruneSeen(await $.clock.now())
    const descriptor = JSON.parse(await $.fs.read(__LEAPMUX_DESCRIPTOR_JSON__))
    if (!safeDescriptor(descriptor)) return
    if (!bridge || bridge.epoch !== descriptor.epoch || bridge.url !== descriptor.url || bridge.token !== descriptor.token) {
      bridge = descriptor
      registered = false
      // Old controller mutations are never replayed into a restarted Worker.
      outcomes.length = 0
    }
    if (!registered) {
      await post($, '/register', { version, activeTurn: activeTurn ?? '', stopped })
      registered = true
    }
    const batch = outcomes.slice(0, 64)
    const response = await post($, '/poll', { activeTurn: activeTurn ?? '', stopped, outcomes: batch })
    outcomes.splice(0, batch.length)
    const acknowledgedAt = await $.clock.now()
    for (const sent of batch) {
      const record = seen.get(sent.deliveryId)
      if (record?.terminal) record.ackedAt = acknowledgedAt
    }
    for (const command of response.commands ?? []) await deliver($, command)
  } catch {
    // Keep the original chat running. Retain outcomes, but never replay sends.
    registered = false
  } finally { polling = false }
}

async function begin($) {
  if (await $.env.get('LEAPMUX_WORKER') === '1') return
  sessionId = await $.session.id()
  if (!validId(sessionId)) { sessionId = null; return }
  instanceId = crypto.randomUUID()
  version = (await $.session.version()).version
  $.clock.every(__LEAPMUX_POLL_MS__, async () => { await tick($) })
  void tick($)
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    if (e.agentId !== undefined) return next(e)
    void begin($).catch(() => {})
    return next(e)
  })
  on('prompt.submit', async ($, e, next) => {
    if (e.origin?.kind === 'composer' || e.origin?.kind === 'bridge') stopped = false
    return next(e)
  })
  on('turn.start', async ($, e, next) => {
    if (e.agentId === undefined) activeTurn = e.turnId
    return next(e)
  })
  on('turn.complete', async ($, e, next) => {
    if (e.agentId === undefined) {
      if (activeTurn === e.turnId) activeTurn = null
      if (e.isAborted && !remoteAborts.has(e.turnId)) stopped = true
      remoteAborts.delete(e.turnId)
    }
    return next(e)
  })
}
