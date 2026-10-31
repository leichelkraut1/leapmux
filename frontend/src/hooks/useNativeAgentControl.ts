import type { Accessor } from 'solid-js'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import type { InspectNativeSessionControlResponse } from '~/generated/proto/leapmux/v1/worker_pb'
import { Code } from '@connectrpc/connect'
import { batch, createEffect, createMemo, createSignal, on, onCleanup, untrack } from 'solid-js'
import { inspectNativeSessionControl, interruptNativeSession, sendNativeSessionInput } from '~/api/workerRpc'
import { nativeConversationKey } from '~/components/chat/nativeConversation'
import { NATIVE_SESSION_CONTROL } from '~/generated/contracts/native-session-control'
import { NativeSessionInputBehavior, NativeSessionInputDisposition, NativeSessionInputState } from '~/generated/proto/leapmux/v1/worker_pb'
import { ChannelError } from '~/lib/channelError'

const REFRESH_INTERVAL_MS = NATIVE_SESSION_CONTROL.PollMilliseconds
const REQUEST_TIMEOUT_MS = 15000

function refusedBeforeDelivery(failure: unknown): boolean {
  return failure instanceof ChannelError && failure.source === 'rpc'
    && (failure.code === Code.FailedPrecondition || failure.code === Code.InvalidArgument || failure.code === Code.PermissionDenied)
}

/** Existing owners only: capability reads never start, resume, or claim a chat. */
export function useNativeAgentControl(ref: Accessor<NativeConversationRef>, online: Accessor<boolean>) {
  const [capabilities, setCapabilities] = createSignal<InspectNativeSessionControlResponse>()
  const [checking, setChecking] = createSignal(false)
  const [expired, setExpired] = createSignal(true)
  const [inspectError, setInspectError] = createSignal('')
  const [pending, setPending] = createSignal<'send' | 'interrupt'>()
  const [receipt, setReceipt] = createSignal('')
  const [error, setError] = createSignal('')
  const [hasComposer, setHasComposer] = createSignal(false)
  const [latestInput, setLatestInput] = createSignal<{ requestId: string, text: string }>()
  const [inputState, setInputState] = createSignal<NativeSessionInputState>()
  let generation = 0
  let scopeGeneration = 0
  let disposed = false
  let controller: AbortController | undefined
  let inspection: Promise<void> | undefined
  let queuedInspection: Promise<void> | undefined
  let expiryTimer: ReturnType<typeof setTimeout> | undefined
  let sendAttempt: { bindingToken: string, text: string, requestId: string, inputBehavior: NativeSessionInputBehavior, uncertain: boolean } | undefined
  let interruptAttempt: { bindingToken: string, turnId: string, requestId: string } | undefined

  const available = createMemo(() => online() && !expired() && !inspectError() && !!capabilities()?.bindingToken)
  const inputBehavior = () => capabilities()?.inputBehavior ?? NativeSessionInputBehavior.UNSPECIFIED
  const knownBehavior = () => inputBehavior() === NativeSessionInputBehavior.QUEUE_NEXT_TURN || inputBehavior() === NativeSessionInputBehavior.SEND_NOW
  const canSend = createMemo(() => available() && !!capabilities()?.canSend && knownBehavior())
  const canInterrupt = createMemo(() => available() && !!capabilities()?.canInterrupt && !!capabilities()?.activeTurnId)
  const recoverableText = createMemo(() => {
    const state = inputState()
    return state === NativeSessionInputState.DROPPED || state === NativeSessionInputState.REJECTED || state === NativeSessionInputState.UNCERTAIN
      ? latestInput()?.text || ''
      : ''
  })
  const unavailableReason = createMemo(() => {
    if (!online())
      return 'Computer offline. Reconnect to continue in the original app.'
    if (inspectError())
      return inspectError()
    if (!capabilities())
      return checking() ? 'Checking the original app connection…' : 'The original app connection is unavailable.'
    if (expired())
      return 'The original app connection has expired. Refresh to check it again.'
    return capabilities()?.unavailableReason || (capabilities()?.canSend && !knownBehavior()
      ? 'The original app has not advertised a supported input behavior.'
      : !canSend() ? 'This session is available for read-only viewing.' : '')
  })

  const cancelProbe = () => {
    controller?.abort()
    controller = undefined
    inspection = undefined
    queuedInspection = undefined
    clearTimeout(expiryTimer)
  }

  function updateInputOutcome(response: InspectNativeSessionControlResponse) {
    const requestId = untrack(latestInput)?.requestId
    const outcome = requestId && response.recentInputs.find(input => input.requestId === requestId)
    if (!outcome || outcome.state === untrack(inputState))
      return
    const labels: Partial<Record<NativeSessionInputState, string>> = {
      [NativeSessionInputState.QUEUED]: 'Message queued in the original app; its turn has not started.',
      [NativeSessionInputState.DISPATCHED]: 'Message handed to the original app. Waiting for its turn to start.',
      [NativeSessionInputState.STARTED]: 'Message started a turn in the original app.',
      [NativeSessionInputState.DROPPED]: 'The original app dropped this message. It was not resent.',
      [NativeSessionInputState.REJECTED]: 'The original app rejected this message. It was not resent.',
      [NativeSessionInputState.UNCERTAIN]: 'Delivery is unconfirmed. Check the original conversation before sending again.',
    }
    const label = labels[outcome.state]
    if (!label)
      return
    batch(() => {
      setInputState(outcome.state)
      setReceipt(outcome.detail ? `${label} ${outcome.detail}` : label)
      setError('')
    })
    if ((outcome.state === NativeSessionInputState.DROPPED || outcome.state === NativeSessionInputState.REJECTED)
      && sendAttempt?.requestId === outcome.requestId) {
      sendAttempt = undefined
    }
  }

  function refresh(): Promise<void> {
    if (disposed || !untrack(online))
      return Promise.resolve()
    if (inspection) {
      if (!queuedInspection) {
        const run = generation
        const queued = inspection.then(async () => {
          if (disposed || run !== generation || !untrack(online))
            return
          if (queuedInspection === queued)
            queuedInspection = undefined
          await refresh()
        }).finally(() => {
          if (queuedInspection === queued)
            queuedInspection = undefined
        })
        queuedInspection = queued
      }
      return queuedInspection
    }
    const current = inspectOnce().finally(() => {
      if (inspection === current)
        inspection = undefined
    })
    inspection = current
    return current
  }

  async function inspectOnce(): Promise<void> {
    if (disposed || !untrack(online))
      return
    const source = untrack(ref)
    const key = nativeConversationKey(source)
    const run = generation
    const current = new AbortController()
    controller = current
    const timeout = setTimeout(() => current.abort(), REQUEST_TIMEOUT_MS)
    let rejectAbort: (() => void) | undefined
    setChecking(true)
    try {
      const aborted = new Promise<never>((_resolve, reject) => {
        rejectAbort = () => reject(new Error('Control inspection cancelled'))
        current.signal.addEventListener('abort', rejectAbort, { once: true })
      })
      const response = await Promise.race([
        inspectNativeSessionControl(source.workerId, { agentProvider: source.agentProvider, sessionId: source.sessionId }, { signal: current.signal }),
        aborted,
      ])
      if (disposed || current.signal.aborted || run !== generation || nativeConversationKey(untrack(ref)) !== key)
        return
      const lifetime = Date.parse(response.expiresAt) - Date.now()
      if (response.sessionId !== source.sessionId
        || ((response.canSend || response.canInterrupt) && (!response.bindingToken || !Number.isFinite(lifetime) || lifetime <= 0))) {
        throw new Error('Unverified native control capability')
      }
      clearTimeout(expiryTimer)
      batch(() => {
        setCapabilities(response)
        setExpired(false)
        setInspectError('')
        if (response.canSend && (response.inputBehavior === NativeSessionInputBehavior.QUEUE_NEXT_TURN || response.inputBehavior === NativeSessionInputBehavior.SEND_NOW))
          setHasComposer(true)
        updateInputOutcome(response)
      })
      if (Number.isFinite(lifetime) && lifetime > 0)
        expiryTimer = setTimeout(setExpired, Math.min(lifetime, 2_147_483_647), true)
    }
    catch {
      if (!disposed && run === generation) {
        batch(() => {
          setExpired(true)
          setInspectError('Control unavailable. Refresh to check the original app; saved history remains available.')
        })
      }
    }
    finally {
      clearTimeout(timeout)
      if (rejectAbort)
        current.signal.removeEventListener('abort', rejectAbort)
      if (run === generation && controller === current) {
        controller = undefined
        setChecking(false)
      }
    }
  }

  async function bounded<T>(operation: Promise<T>): Promise<T> {
    let timeout: ReturnType<typeof setTimeout> | undefined
    try {
      return await Promise.race([operation, new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error('Native control acknowledgement timed out')), REQUEST_TIMEOUT_MS)
      })])
    }
    finally {
      clearTimeout(timeout)
    }
  }

  async function send(text: string): Promise<boolean> {
    if (!untrack(canSend) || untrack(pending) || !text.trim() || Date.parse(untrack(capabilities)!.expiresAt) <= Date.now())
      return false
    if (new TextEncoder().encode(text).length > NATIVE_SESSION_CONTROL.MaxTextBytes) {
      setError(`Message exceeds the ${NATIVE_SESSION_CONTROL.MaxTextBytes.toLocaleString()} byte limit. Shorten it before sending.`)
      return false
    }
    if (text.includes('\0')) {
      setError('Remove NUL characters from this message before sending.')
      return false
    }
    const source = untrack(ref)
    const scope = scopeGeneration
    const bindingToken = untrack(capabilities)!.bindingToken
    // A manual retry of the same unconfirmed input retains its idempotency ID.
    if (!sendAttempt || sendAttempt.text !== text)
      sendAttempt = { bindingToken, text, requestId: crypto.randomUUID(), inputBehavior: untrack(inputBehavior), uncertain: false }
    const attempt = sendAttempt
    batch(() => {
      setPending('send')
      setReceipt('')
      setError('')
      setLatestInput({ requestId: attempt.requestId, text })
      setInputState(undefined)
    })
    try {
      const response = await bounded(sendNativeSessionInput(source.workerId, {
        agentProvider: source.agentProvider,
        sessionId: source.sessionId,
        bindingToken: attempt.bindingToken,
        requestId: attempt.requestId,
        text: attempt.text,
        inputBehavior: attempt.inputBehavior,
      }))
      if (disposed || scope !== scopeGeneration)
        return false
      if (response.sessionId !== source.sessionId || response.requestId !== attempt.requestId
        || (response.disposition !== NativeSessionInputDisposition.ACCEPTED && response.disposition !== NativeSessionInputDisposition.QUEUED)
        || (attempt.inputBehavior === NativeSessionInputBehavior.SEND_NOW && response.disposition === NativeSessionInputDisposition.QUEUED)) {
        throw new Error('Unverified native input acknowledgement')
      }
      if (untrack(inputState) === undefined) {
        setReceipt(response.disposition === NativeSessionInputDisposition.QUEUED
          ? 'Message queued in the original app for its next turn.'
          : 'Message accepted by the original app.')
      }
      sendAttempt = undefined
      void refresh()
      return true
    }
    catch (failure) {
      if (!disposed && scope === scopeGeneration) {
        const outcome = untrack(inputState)
        if (outcome === NativeSessionInputState.QUEUED || outcome === NativeSessionInputState.DISPATCHED || outcome === NativeSessionInputState.STARTED) {
          sendAttempt = undefined
          return true
        }
        if (refusedBeforeDelivery(failure) && !attempt.uncertain) {
          sendAttempt = undefined
          setInputState(NativeSessionInputState.REJECTED)
          setError('Message was not sent. The request was refused; refresh the connection or check your access before trying again.')
          return false
        }
        if (outcome === undefined) {
          attempt.uncertain = true
          setInputState(NativeSessionInputState.UNCERTAIN)
          setError('The original app did not confirm delivery. Your text is kept. Check the conversation before sending again.')
        }
      }
      return false
    }
    finally {
      if (!disposed && scope === scopeGeneration)
        setPending(undefined)
    }
  }

  async function interrupt(): Promise<void> {
    if (!untrack(canInterrupt) || untrack(pending) || Date.parse(untrack(capabilities)!.expiresAt) <= Date.now())
      return
    const source = untrack(ref)
    const scope = scopeGeneration
    const capability = untrack(capabilities)!
    const bindingToken = capability.bindingToken
    const turnId = capability.activeTurnId
    if (!interruptAttempt || interruptAttempt.bindingToken !== bindingToken || interruptAttempt.turnId !== turnId)
      interruptAttempt = { bindingToken, turnId, requestId: crypto.randomUUID() }
    const attempt = interruptAttempt
    batch(() => {
      setPending('interrupt')
      setReceipt('')
      setError('')
    })
    try {
      const response = await bounded(interruptNativeSession(source.workerId, {
        agentProvider: source.agentProvider,
        sessionId: source.sessionId,
        bindingToken,
        requestId: attempt.requestId,
        expectedTurnId: turnId,
      }))
      if (disposed || scope !== scopeGeneration)
        return
      if (response.sessionId !== source.sessionId || response.requestId !== attempt.requestId)
        throw new Error('Unverified native interruption acknowledgement')
      setReceipt('Interrupt accepted by the original app for this turn.')
      interruptAttempt = undefined
      void refresh()
    }
    catch {
      if (!disposed && scope === scopeGeneration)
        setError('The original app did not confirm the interrupt. Check its current turn before trying again.')
    }
    finally {
      if (!disposed && scope === scopeGeneration)
        setPending(undefined)
    }
  }

  createEffect(on(() => [nativeConversationKey(ref()), online()] as const, ([key], previous) => {
    generation++
    cancelProbe()
    batch(() => {
      setChecking(false)
      setExpired(true)
      setInspectError('')
      if (!previous || previous[0] !== key) {
        scopeGeneration++
        setCapabilities(undefined)
        setHasComposer(false)
        setLatestInput(undefined)
        setInputState(undefined)
        setReceipt('')
        setError('')
        setPending(undefined)
        sendAttempt = undefined
        interruptAttempt = undefined
      }
    })
    if (online())
      void refresh()
  }))
  const timer = setInterval(() => {
    if (!inspection)
      void refresh()
  }, REFRESH_INTERVAL_MS)
  onCleanup(() => {
    disposed = true
    generation++
    clearInterval(timer)
    cancelProbe()
  })
  return { capabilities, checking, canSend, canInterrupt, hasComposer, inputBehavior, unavailableReason, pending, receipt, error, recoverableText, refresh, send, interrupt }
}
