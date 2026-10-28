import type { Accessor } from 'solid-js'
import type { NativeConversationRecord, NativeConversationRef } from '~/components/chat/nativeConversation'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { toBinary } from '@bufbuild/protobuf'
import { batch, createEffect, createMemo, createSignal, on, onCleanup, onMount, untrack } from 'solid-js'
import { readNativeConversation } from '~/api/workerRpc'
import { createMessageContextResolver } from '~/components/chat/messageContextResolver'
import { nativeConversationKey } from '~/components/chat/nativeConversation'
import { pluginFor } from '~/components/chat/providers/registry'
import { NATIVE_CONVERSATION_DIRECTION } from '~/generated/contracts/native-conversations'
import { AgentChatMessageSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { messageSpanKey } from '~/lib/messageSpan'
import { createSpanIndex } from '~/stores/chatSpanIndex'

const REFRESH_INTERVAL_MS = 5000
const REQUEST_TIMEOUT_MS = 15000
const PAGE_LIMIT = 64
type Direction = typeof NATIVE_CONVERSATION_DIRECTION[keyof typeof NATIVE_CONVERSATION_DIRECTION]

function sameBytes(left: Uint8Array, right: Uint8Array): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index])
}

function isNativeJsonBytes(value: unknown): value is Uint8Array {
  // Protobuf byte views can come from another window or runtime realm.
  return ArrayBuffer.isView(value) && Object.prototype.toString.call(value) === '[object Uint8Array]'
}

/** A private read-only window; it never joins the managed-agent store or event stream. */
export function useNativeAgentTranscript(ref: Accessor<NativeConversationRef>, online: Accessor<boolean>) {
  const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
  const [version, setVersion] = createSignal(0)
  const [title, setTitle] = createSignal('')
  const [workingDir, setWorkingDir] = createSignal('')
  const [observedAt, setObservedAt] = createSignal('')
  const [partialReason, setPartialReason] = createSignal('')
  const [error, setError] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [fetchingOlder, setFetchingOlder] = createSignal(false)
  const [hasOlder, setHasOlder] = createSignal(false)
  const [hasNewer, setHasNewer] = createSignal(false)
  const [loaded, setLoaded] = createSignal(false)
  const [sourceChanged, setSourceChanged] = createSignal(false)
  const observers = new Set<(message: AgentChatMessage) => void>()
  const contentVersions = new Map<string, number>()
  let records: NativeConversationRecord[] = []
  let revision = ''
  let sourceFormat = ''
  let olderCursor = ''
  let newerCursor = ''
  let generation = 0
  let pending: Promise<void> | undefined
  let controller: AbortController | undefined
  let disposed = false

  const spans = createMemo(() => {
    const index = createSpanIndex()
    index.reindex(nativeConversationKey(ref()), messages())
    return index
  })

  function project(next: NativeConversationRecord[], format: string): AgentChatMessage[] {
    const source = ref()
    const adapter = pluginFor(source.agentProvider)?.transcript.projectNativeConversation
    if (!adapter)
      throw new Error('Native history is not supported by this provider')
    const previous = new Map(untrack(messages).map(message => [message.id, message]))
    const ids = new Set<string>()
    const seqs = new Set<bigint>()
    const projected = adapter(next, format, source.sessionId).sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0)
    return projected.map((message) => {
      if (!message.id || ids.has(message.id) || seqs.has(message.seq) || message.seq <= 0n
        || message.agentSessionId !== source.sessionId || message.agentProvider !== source.agentProvider) {
        throw new Error('Native history projection has inconsistent identities')
      }
      ids.add(message.id)
      seqs.add(message.seq)
      const before = previous.get(message.id)
      if (before && sameBytes(toBinary(AgentChatMessageSchema, before), toBinary(AgentChatMessageSchema, message)))
        return before
      if (before)
        contentVersions.set(message.id, (contentVersions.get(message.id) ?? 0) + 1)
      return message
    })
  }

  function cancel(): void {
    generation++
    controller?.abort()
    controller = undefined
    pending = undefined
    setBusy(false)
    setFetchingOlder(false)
  }

  async function load(direction: Direction): Promise<void> {
    if (disposed || !untrack(online))
      return
    if (pending)
      return pending
    if (direction === NATIVE_CONVERSATION_DIRECTION.Before && (!olderCursor || !untrack(hasOlder)))
      return
    const source = untrack(ref)
    const key = nativeConversationKey(source)
    const run = generation
    const current = new AbortController()
    controller = current
    const timeout = setTimeout(() => current.abort(), REQUEST_TIMEOUT_MS)
    setBusy(true)
    setFetchingOlder(direction === NATIVE_CONVERSATION_DIRECTION.Before)
    setError('')
    const task = (async () => {
      await Promise.resolve()
      let rejectAborted: (() => void) | undefined
      const aborted = new Promise<never>((_resolve, reject) => {
        rejectAborted = () => reject(new Error('Native history read cancelled'))
        current.signal.addEventListener('abort', rejectAborted, { once: true })
      })
      try {
        let pageDirection = direction
        let cursor = direction === NATIVE_CONVERSATION_DIRECTION.Before ? olderCursor : direction === NATIVE_CONVERSATION_DIRECTION.After ? newerCursor : ''
        let response = await Promise.race([
          readNativeConversation(source.workerId, { agentProvider: source.agentProvider, sessionId: source.sessionId, cursor, direction: pageDirection, limit: PAGE_LIMIT }, { signal: current.signal }),
          aborted,
        ])
        if (response.resetRequired) {
          pageDirection = NATIVE_CONVERSATION_DIRECTION.Latest
          cursor = ''
          response = await Promise.race([
            readNativeConversation(source.workerId, { agentProvider: source.agentProvider, sessionId: source.sessionId, cursor, direction: pageDirection, limit: PAGE_LIMIT }, { signal: current.signal }),
            aborted,
          ])
          if (response.resetRequired)
            throw new Error('Native history keeps changing')
        }
        if (disposed || current.signal.aborted || generation !== run || nativeConversationKey(untrack(ref)) !== key)
          return
        if (response.sessionId !== source.sessionId || !response.format || !response.revision
          || (revision && response.revision !== revision && pageDirection !== NATIVE_CONVERSATION_DIRECTION.Latest)
          || (sourceFormat && response.format !== sourceFormat
            && (response.revision === revision || pageDirection !== NATIVE_CONVERSATION_DIRECTION.Latest))) {
          throw new Error('Native history source changed')
        }
        if (pageDirection === NATIVE_CONVERSATION_DIRECTION.Before && response.hasOlder && response.olderCursor === cursor)
          throw new Error('Native history cursor did not advance')
        if (pageDirection === NATIVE_CONVERSATION_DIRECTION.After && response.hasNewer && response.newerCursor === cursor)
          throw new Error('Native history cursor did not advance')
        const updatePageMetadata = () => {
          if (pageDirection !== NATIVE_CONVERSATION_DIRECTION.After) {
            olderCursor = response.olderCursor
            setHasOlder(response.hasOlder)
          }
          if (pageDirection !== NATIVE_CONVERSATION_DIRECTION.Before) {
            newerCursor = response.newerCursor
            setHasNewer(response.hasNewer)
          }
          setTitle(response.displayName || source.displayName || source.sessionId)
          setWorkingDir(response.workingDir)
          setObservedAt(response.observedAt)
          setPartialReason(response.partialReason)
          setLoaded(true)
        }
        // Empty tail pages refresh source metadata, not transcript content. In
        // particular, do not reparse or reserialize the entire loaded history.
        if (pageDirection === NATIVE_CONVERSATION_DIRECTION.After && revision === response.revision
          && sourceFormat === response.format && response.records.length === 0) {
          batch(updatePageMetadata)
          return
        }
        const byId = new Map(pageDirection === NATIVE_CONVERSATION_DIRECTION.Latest ? [] : records.map(record => [record.id, record]))
        for (const record of response.records) {
          if (!record.id || record.seq <= 0n || !isNativeJsonBytes(record.nativeJson))
            throw new Error('Native history contains an invalid record')
          const before = byId.get(record.id)
          byId.set(record.id, before && before.seq === record.seq && sameBytes(before.nativeJson, record.nativeJson) ? before : record)
        }
        const next = [...byId.values()].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0)
        const projected = project(next, response.format)
        const oldIds = new Map(untrack(messages).map(message => [message.id, message]))
        if (revision && revision !== response.revision)
          setSourceChanged(true)
        records = next
        revision = response.revision
        sourceFormat = response.format
        batch(() => {
          updatePageMetadata()
          setMessages(projected)
          setVersion(value => value + 1)
        })
        for (const message of projected) {
          if (oldIds.get(message.id) !== message) {
            for (const observer of observers)
              observer(message)
          }
        }
      }
      catch {
        if (!disposed && generation === run && nativeConversationKey(untrack(ref)) === key) {
          setError(untrack(loaded)
            ? 'This computer could not provide updated chat history. The last loaded messages remain available; refresh to try again.'
            : 'Could not load saved chat history from this computer. Refresh to try again.')
        }
      }
      finally {
        clearTimeout(timeout)
        if (rejectAborted)
          current.signal.removeEventListener('abort', rejectAborted)
        if (generation === run && controller === current) {
          controller = undefined
          pending = undefined
          setBusy(false)
          setFetchingOlder(false)
        }
      }
    })()
    pending = task
    return task
  }

  const refresh = () => load(loaded() && newerCursor ? NATIVE_CONVERSATION_DIRECTION.After : NATIVE_CONVERSATION_DIRECTION.Latest)
  const loadOlder = () => load(NATIVE_CONVERSATION_DIRECTION.Before)
  const resolver = createMemo(on(() => nativeConversationKey(ref()), scopeKey => createMessageContextResolver({
    scopeKey,
    messages,
    messageVersion: version,
    contentVersion: id => contentVersions.get(id) ?? 0,
    spanMessage: (identity, side) => side === 'request'
      ? spans().getRequestMessage(nativeConversationKey(ref()), identity)
      : spans().getResultMessage(nativeConversationKey(ref()), identity),
    messageBySeq: seq => messages().find(message => message.seq === seq),
    // Rendering a result must not eagerly drain a large native history. Earlier
    // context enters the same resolver when the reader loads older history.
    fetchSpan: async identity => messages().filter(message => messageSpanKey(message) === messageSpanKey(identity)),
    fetchMessage: async seq => messages().find(message => message.seq === seq),
    fetchFileImage: async () => { throw new Error('This native chat has no scoped file image reader') },
    subscribe: (observer) => {
      observers.add(observer)
      return () => observers.delete(observer)
    },
    backgroundTask: () => undefined,
    progress: () => undefined,
  })))

  createEffect(on(() => [nativeConversationKey(ref()), online()] as const, ([key], previous) => {
    cancel()
    if (!previous || previous[0] !== key) {
      records = []
      revision = ''
      sourceFormat = ''
      olderCursor = ''
      newerCursor = ''
      contentVersions.clear()
      batch(() => {
        setMessages([])
        setTitle(ref().displayName || ref().sessionId)
        setWorkingDir('')
        setObservedAt('')
        setPartialReason('')
        setLoaded(false)
        setHasOlder(false)
        setHasNewer(false)
        setSourceChanged(false)
        setError('')
        setVersion(value => value + 1)
      })
    }
    if (online())
      void refresh()
  }))
  onMount(() => {
    const timer = setInterval(() => void refresh(), REFRESH_INTERVAL_MS)
    onCleanup(() => clearInterval(timer))
  })
  onCleanup(() => {
    disposed = true
    cancel()
    observers.clear()
  })

  return { messages, version, title, workingDir, observedAt, partialReason, error, busy, fetchingOlder, hasOlder, hasNewer, loaded, sourceChanged, resolver, refresh, loadOlder }
}
