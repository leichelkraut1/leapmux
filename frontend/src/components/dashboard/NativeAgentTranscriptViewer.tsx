import type { Component } from 'solid-js'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import { Show } from 'solid-js'
import { ChatView } from '~/components/chat/ChatView'
import { nativeConversationKey } from '~/components/chat/nativeConversation'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Dialog } from '~/components/common/Dialog'
import { useNativeAgentTranscript } from '~/hooks/useNativeAgentTranscript'
import * as styles from './NativeAgentTranscriptViewer.css'

export const NativeAgentTranscriptViewer: Component<{
  conversation: NativeConversationRef
  workerName?: string
  online: boolean
  onClose: () => void
}> = (props) => {
  const transcript = useNativeAgentTranscript(() => props.conversation, () => props.online)
  return (
    <Dialog title={transcript.title() || props.conversation.sessionId} huge tall data-testid="native-agent-transcript" onClose={props.onClose}>
      <div class={styles.viewer}>
        <div class={styles.heading}>
          <p class={styles.description}>{`${agentProviderLabel(props.conversation.agentProvider)} · ${props.workerName ?? props.conversation.workerName}`}</p>
          <button type="button" disabled={transcript.busy() || !props.online} onClick={() => void transcript.refresh()} aria-label="Refresh native chat history">
            {transcript.busy() ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
        <p class={styles.banner}>Read only · This chat remains controlled by its original app. Messages and tools are shown without taking control.</p>
        <Show when={transcript.observedAt()}>
          {time => (
            <p class={styles.description}>
              Last checked
              {' '}
              <time dateTime={time()}>{new Date(time()).toLocaleString()}</time>
            </p>
          )}
        </Show>
        <Show when={!props.online}>
          <p class={styles.banner} role="status">{transcript.loaded() ? 'Computer offline. Showing the last loaded history.' : 'Computer offline. Reconnect to load saved chat history.'}</p>
        </Show>
        <Show when={transcript.error()}>{error => <p class={styles.banner} role="status">{error()}</p>}</Show>
        <Show when={transcript.partialReason()}>{reason => <p class={styles.banner} role="status">{reason()}</p>}</Show>
        <Show when={transcript.sourceChanged()}>
          <p class={styles.description}>The native history changed. Showing its updated messages.</p>
        </Show>
        <Show when={transcript.hasOlder()}>
          <p class={styles.description}>Earlier messages and tool context are available in older history.</p>
          <button type="button" class={styles.historyAction} disabled={transcript.busy() || !props.online} onClick={() => void transcript.loadOlder()}>
            {transcript.fetchingOlder() ? 'Loading older history…' : 'Load older history'}
          </button>
        </Show>
        <Show when={!transcript.loaded() && transcript.busy()}>
          <p role="status">Loading native chat history…</p>
        </Show>
        <Show when={transcript.loaded() && transcript.messages().length === 0}>
          <p role="status">No readable messages in this history page.</p>
        </Show>
        <div class={styles.history} data-testid="native-transcript-history">
          <Show when={transcript.messages().length > 0}>
            <ChatView
              agentId={nativeConversationKey(props.conversation)}
              messages={transcript.messages()}
              messageVersion={transcript.version()}
              tabActive
              workingDir={transcript.workingDir()}
              messageContext={transcript.resolver()}
              pagination={{
                hasOlderMessages: transcript.hasOlder() && props.online,
                fetchingOlder: transcript.fetchingOlder(),
                onLoadOlderMessages: () => transcript.loadOlder(),
                hasNewerMessages: transcript.hasNewer() && props.online,
                fetchingNewer: transcript.busy() && !transcript.fetchingOlder(),
                onLoadNewerMessages: () => transcript.refresh(),
                onJumpToLatest: () => transcript.refresh(),
              }}
            />
          </Show>
        </div>
      </div>
    </Dialog>
  )
}
