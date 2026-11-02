import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

const ROUTE_PROVIDERS: Readonly<Record<string, AgentProvider>> = {
  codex: AgentProvider.CODEX,
  claude: AgentProvider.CLAUDE_CODE,
}

/** An unchanged provider record; sequence is its one-based byte offset. */
export interface NativeConversationRecord {
  id: string
  seq: bigint
  nativeJson: Uint8Array
}

/** A native session on one computer, independent of any LeapMux agent or tab. */
export interface NativeConversationRef {
  workerId: string
  workerName: string
  agentProvider: AgentProvider
  sessionId: string
  displayName?: string
}

export function nativeConversationKey(ref: NativeConversationRef): string {
  return JSON.stringify([ref.workerId, ref.agentProvider, ref.sessionId])
}

/** Links name a registered computer and opaque native UUID; never a file path. */
export function nativeConversationFromParams(
  params: { dashboard?: unknown, externalWorker?: unknown, externalProvider?: unknown, externalSession?: unknown },
  workers: readonly { workerId: string, workerName: string }[],
): NativeConversationRef | undefined {
  if (params.dashboard !== 'agents' || typeof params.externalWorker !== 'string'
    || typeof params.externalProvider !== 'string' || !Object.hasOwn(ROUTE_PROVIDERS, params.externalProvider)
    || typeof params.externalSession !== 'string'
    || !/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i.test(params.externalSession)) {
    return undefined
  }
  const worker = workers.find(item => item.workerId === params.externalWorker)
  const agentProvider = ROUTE_PROVIDERS[params.externalProvider]
  return worker && agentProvider !== undefined ? { ...worker, agentProvider, sessionId: params.externalSession } : undefined
}

export function nativeConversationRouteProvider(provider: AgentProvider): string | undefined {
  return Object.keys(ROUTE_PROVIDERS).find(key => ROUTE_PROVIDERS[key] === provider)
}
