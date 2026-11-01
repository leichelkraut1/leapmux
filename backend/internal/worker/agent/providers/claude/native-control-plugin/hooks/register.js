// Claude Mods API; background control polling and exact permission-request waits.
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
const heldApprovals = new Map()

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
      heldApprovals.clear()
    }
    if (!registered) {
      await post($, '/register', { version, activeTurn: activeTurn ?? '', stopped })
      registered = true
    }
    const batch = outcomes.slice(0, 64)
    const response = await post($, '/poll', { activeTurn: activeTurn ?? '', stopped, outcomes: batch,
      approvalIds: [...heldApprovals.keys()] })
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

// This event is the real permission request, after Claude's normal checks. It
// is not tool.check, and it does not create permission rules or change modes.
// Each bounded HTTP wait is a Mods API call, so it does not consume hook CPU
// time. A healthy bridge keeps the same invocation pending until answered.
// Bridge loss returns no decision and keeps the native flow.
async function approval($, e, next) {
  if (!sessionId || !registered || !bridge || !activeTurn || stopped || e.agent_id !== undefined ||
      e.session_id !== sessionId || heldApprovals.size >= __LEAPMUX_MAX_APPROVALS__) return next(e)
  const title = 'Tool approval: ' + e.tool_name
  const detail = JSON.stringify({ tool: e.tool_name, input: e.tool_input, cwd: e.cwd }, null, 2)
  if (new TextEncoder().encode(title).length > __LEAPMUX_APPROVAL_TEXT_BYTES__ ||
      new TextEncoder().encode(detail).length > __LEAPMUX_APPROVAL_DETAIL_BYTES__ ||
      title.includes('\0') || detail.includes('\0')) return next(e)
  const id = crypto.randomUUID()
  const turnId = activeTurn
  const epoch = bridge.epoch
  heldApprovals.set(id, { turnId, epoch })
  const abandoned = () => {
    heldApprovals.delete(id)
    void tick($)
  }
  next.signal.addEventListener('abort', abandoned, { once: true })
  try {
    let result
    do {
      result = await post($, '/approval', {
        activeTurn: turnId, stopped,
        approval: { protocol: 1, id, turnId, title, detail },
      })
      if (next.signal.aborted || !heldApprovals.has(id) || bridge.epoch !== epoch || activeTurn !== turnId) return next(e)
    } while (result.waiting === true)
    const command = result.command
    if (next.signal.aborted || !heldApprovals.has(id) || bridge.epoch !== epoch || activeTurn !== turnId ||
        !command || command.kind !== 'approval' || command.instanceId !== instanceId || command.turnId !== turnId ||
        !validId(command.requestId) || !validId(command.deliveryId) || command.deliveryId.length !== 43 ||
        !validId(command.promptId) || seen.has(command.deliveryId) ||
        (command.optionId !== 'allow_once' && command.optionId !== 'deny' && command.optionId !== 'original_app')) return next(e)
    seen.set(command.deliveryId, { terminal: false, ackedAt: null })
    // A delivery acknowledgement means this hook returned the explicit answer,
    // not that Claude allowed it against policy or that the tool ran.
    outcome(command, 'answered', undefined, turnId)
    if (command.optionId === 'original_app') return next(e)
    return { decision: command.optionId === 'allow_once'
      ? { behavior: 'allow' }
      : { behavior: 'deny', message: 'The user declined this tool request in LeapMux.' } }
  } catch {
    return next(e)
  } finally {
    next.signal.removeEventListener('abort', abandoned)
    heldApprovals.delete(id)
    void tick($)
  }
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
      for (const [id, pending] of heldApprovals) {
        if (pending.turnId === e.turnId) heldApprovals.delete(id)
      }
    }
    return next(e)
  })
  on('classic.PermissionRequest', approval).catch(($, e, next) => next(e))
}
