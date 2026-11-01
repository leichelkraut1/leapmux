import type { Component } from 'solid-js'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import { createEffect, createMemo, createSignal, createUniqueId, For, on, Show } from 'solid-js'
import { ChatView } from '~/components/chat/ChatView'
import { nativeConversationKey } from '~/components/chat/nativeConversation'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Dialog } from '~/components/common/Dialog'
import { NATIVE_SESSION_CONTROL } from '~/generated/contracts/native-session-control'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'
import { NativeSessionInputBehavior } from '~/generated/proto/leapmux/v1/worker_pb'
import { useNativeAgentControl } from '~/hooks/useNativeAgentControl'
import { useNativeAgentTranscript } from '~/hooks/useNativeAgentTranscript'
import * as styles from './NativeAgentTranscriptViewer.css'

export const NativeAgentTranscriptViewer: Component<{
  conversation: NativeConversationRef
  workerName?: string
  online: boolean
  onClose: () => void
}> = (props) => {
  const transcript = useNativeAgentTranscript(() => props.conversation, () => props.online)
  const control = useNativeAgentControl(() => props.conversation, () => props.online)
  const [draft, setDraft] = createSignal('')
  const draftBytes = createMemo(() => new TextEncoder().encode(draft()).length)
  const overLimit = () => draftBytes() > NATIVE_SESSION_CONTROL.MaxTextBytes
  const hasNul = () => draft().includes('\0')
  const sendNow = () => control.inputBehavior() === NativeSessionInputBehavior.SEND_NOW
  const sendDescription = () => sendNow()
    ? 'If this chat is running, this sends input to its current turn.'
    : 'Messages queue in the original app for its next turn.'
  const editorId = createUniqueId()
  const hintId = createUniqueId()
  createEffect(on(() => nativeConversationKey(props.conversation), () => setDraft('')))
  const send = async () => {
    const text = draft()
    if (await control.send(text)) {
      if (draft() === text)
        setDraft('')
      void transcript.refresh()
    }
  }
  const activity = () => {
    switch (control.capabilities()?.activityState) {
      case AgentActivityState.WORKING: return 'Working'
      case AgentActivityState.WAITING_FOR_USER: return 'Waiting on you'
      case AgentActivityState.IDLE: return 'Idle'
      default: return 'Status unavailable'
    }
  }
  return (
    <Dialog title={transcript.title() || props.conversation.sessionId} huge tall data-testid="native-agent-transcript" onClose={props.onClose}>
      <div class={styles.viewer}>
        <div class={styles.heading}>
          <p class={styles.description}>{`${agentProviderLabel(props.conversation.agentProvider)} · ${props.workerName ?? props.conversation.workerName}`}</p>
          <button
            type="button"
            disabled={transcript.busy() || !props.online}
            onClick={() => {
              void transcript.refresh()
              void control.refresh()
            }}
            aria-label="Refresh native chat history"
          >
            {transcript.busy() ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
        <Show
          when={control.connected()}
          fallback={(
            <p class={styles.banner}>
              Read only · This chat remains controlled by its original app.
              {control.unavailableReason()}
            </p>
          )}
        >
          <p class={styles.banner}>
            Connected to the original conversation ·
            {' '}
            {activity()}
            <Show when={control.hasComposer()}><span class={styles.connectionDescription}>{sendDescription()}</span></Show>
          </p>
        </Show>
        <Show when={control.capabilities()?.ownerLabel}>
          {owner => (
            <p class={styles.description}>
              Owner:
              {' '}
              {owner()}
              {control.capabilities()?.transportLabel ? ` · ${control.capabilities()!.transportLabel}` : ''}
            </p>
          )}
        </Show>
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
        <Show when={control.pendingApprovals().length > 0}>
          <div class={styles.approvals} aria-label="Pending approvals" data-testid="native-approval-prompts">
            <For each={control.pendingApprovals().map(approval => approval.promptId)}>
              {promptId => (
                <Show when={control.pendingApprovals().find(approval => approval.promptId === promptId)}>
                  {approval => (
                    <section class={styles.approval} aria-label={`Approval: ${approval().title}`} data-testid="native-approval-prompt" data-prompt-id={promptId}>
                      <p class={styles.description}>Waiting for your approval</p>
                      <h3 class={styles.approvalTitle}>{approval().title}</h3>
                      <pre class={styles.approvalDetail}>{approval().detail}</pre>
                      <div class={styles.approvalOptions}>
                        <For each={approval().options.map(option => option.id)}>
                          {optionId => (
                            <Show when={approval().options.find(option => option.id === optionId)}>
                              {option => (
                                <div class={styles.approvalOption}>
                                  <button
                                    class={styles.approvalChoice}
                                    type="button"
                                    disabled={!control.canAnswerApproval(promptId)}
                                    onClick={() => void control.answerApproval(promptId, optionId)}
                                  >
                                    {option().label}
                                  </button>
                                  <Show when={option().description}>{description => <p class={styles.description}>{description()}</p>}</Show>
                                </div>
                              )}
                            </Show>
                          )}
                        </For>
                      </div>
                      <Show
                        when={control.approvalAnswerState(promptId)}
                        fallback={(
                          <Show when={!control.canAnswerApproval(promptId)}>
                            <p class={styles.description} role="status">{approval().unavailableReason || control.unavailableReason() || 'Refresh the connection to answer this prompt.'}</p>
                          </Show>
                        )}
                      >
                        {state => (
                          <p class={styles.description} role="status">
                            {state() === 'sending'
                              ? 'Sending your answer…'
                              : state() === 'accepted'
                                ? 'Answer submitted to the original app. Waiting for the prompt to close.'
                                : 'Answer unconfirmed. It will not be resent; check the original app.'}
                          </p>
                        )}
                      </Show>
                    </section>
                  )}
                </Show>
              )}
            </For>
          </div>
        </Show>
        <Show when={control.hasComposer() || control.capabilities()?.canInterrupt}>
          <form
            class={styles.composer}
            onSubmit={(event) => {
              event.preventDefault()
              void send()
            }}
          >
            <Show when={control.hasComposer()}>
              <label class={styles.description} for={editorId}>Continue this conversation</label>
              <textarea
                id={editorId}
                class={styles.editor}
                aria-label="Message to original chat"
                aria-describedby={hintId}
                aria-invalid={overLimit() || hasNul()}
                placeholder="Write a message to the original chat…"
                rows={3}
                value={draft()}
                onInput={event => setDraft(event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (!event.isComposing && event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
                    event.preventDefault()
                    void send()
                  }
                }}
              />
              <p id={hintId} class={styles.description}>
                {sendDescription()}
                {' '}
                {draftBytes().toLocaleString()}
                {' '}
                /
                {NATIVE_SESSION_CONTROL.MaxTextBytes.toLocaleString()}
                {' '}
                bytes · Plain text · Ctrl/⌘+Enter to
                {sendNow() ? 'send now' : 'send'}
                <Show when={hasNul()}> · Remove NUL characters before sending.</Show>
              </p>
            </Show>
            <div class={styles.actions}>
              <Show when={control.recoverableText() && !draft()}>
                <button type="button" onClick={() => setDraft(control.recoverableText())}>Restore message to editor</button>
              </Show>
              <Show when={control.capabilities()?.canInterrupt}>
                <button type="button" disabled={!control.canInterrupt() || !!control.pending()} onClick={() => void control.interrupt()}>
                  {control.pending() === 'interrupt' ? 'Interrupting…' : 'Interrupt current turn'}
                </button>
              </Show>
              <Show when={control.hasComposer()}>
                <button type="submit" disabled={!control.canSend() || !!control.pending() || !draft().trim() || overLimit() || hasNul()}>
                  {control.pending() === 'send' ? 'Sending…' : sendNow() ? 'Send now' : 'Send to original chat'}
                </button>
              </Show>
            </div>
          </form>
        </Show>
        <Show when={control.receipt()}>{receipt => <p class={styles.description} role="status">{receipt()}</p>}</Show>
        <Show when={control.error()}>{error => <p class={styles.banner} role="alert">{error()}</p>}</Show>
      </div>
    </Dialog>
  )
}
