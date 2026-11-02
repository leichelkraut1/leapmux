import type { Component } from 'solid-js'
import type { NativeConversationRef } from '~/components/chat/nativeConversation'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createEffect, createMemo, createSignal, For, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Dialog } from '~/components/common/Dialog'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './AttachNativeConversationDialog.css'

/** An existing organizational destination; never a request to create a worktree. */
export interface NativeConversationDestination {
  id: string
  workspaceId: string
  workspaceName: string
  workerId: string
  workerName: string
  workingDir: string
  label: string
  branchName?: string
  disabledReason?: string
}

export interface AttachNativeConversationDialogProps {
  conversation: NativeConversationRef
  destinations: NativeConversationDestination[]
  busy?: boolean
  error?: string
  onConfirm: (destination: NativeConversationDestination) => void
  onClose: () => void
}

export const AttachNativeConversationDialog: Component<AttachNativeConversationDialogProps> = (props) => {
  const [workspaceId, setWorkspaceId] = createSignal('')
  const [destinationId, setDestinationId] = createSignal('')
  const [filter, setFilter] = createSignal('')
  let worktreeMenu: HTMLElement | undefined
  const workspaces = createMemo(() => {
    const byId = new Map<string, string>()
    for (const destination of props.destinations)
      byId.set(destination.workspaceId, destination.workspaceName)
    return [...byId].map(([value, label]) => ({ value, label }))
  })
  const worktrees = createMemo(() => props.destinations.filter(destination => destination.workspaceId === workspaceId()))
  const destination = createMemo(() => worktrees().find(item => item.id === destinationId()))
  const filteredWorktrees = createMemo(() => {
    const staticQuery = filter().trim().toLocaleLowerCase()
    return worktrees().filter(item => !staticQuery || `${item.label} ${item.branchName ?? ''} ${item.workingDir} ${item.workerName}`.toLocaleLowerCase().includes(staticQuery))
  })
  const selectWorkspace = (id: string) => {
    setWorkspaceId(id)
    const options = props.destinations.filter(destination => destination.workspaceId === id)
    setDestinationId((options.find(option => !option.disabledReason) ?? options[0])?.id ?? '')
    setFilter('')
  }
  createEffect(() => {
    // Initialize when destinations arrive. A removed selection is deliberately
    // not replaced: submitting must never silently choose another worktree.
    if (workspaceId())
      return
    const first = props.destinations.find(item => !item.disabledReason) ?? props.destinations[0]
    if (first)
      selectWorkspace(first.workspaceId)
  })
  const confirm = () => {
    const current = destination()
    if (!props.busy && current && !current.disabledReason)
      props.onConfirm(current)
  }

  return (
    <Dialog title="Add chat to worktree" busy={!!props.busy} onClose={props.onClose} data-testid="attach-native-conversation-dialog">
      <form
        onSubmit={(event) => {
          event.preventDefault()
          confirm()
        }}
        // Native binding runs before Dialog's native keydown listener. A
        // delegated Solid handler would prevent default after Dialog submitted.
        on:keydown={(event) => {
          if (event.key === 'Enter' && event.target instanceof HTMLInputElement && event.target.closest('[popover]'))
            event.preventDefault()
        }}
      >
        <section class={styles.fields}>
          <p class={styles.description}>
            Source:
            {' '}
            {agentProviderLabel(props.conversation.agentProvider)}
            {' · '}
            {props.conversation.workerName || props.conversation.workerId}
          </p>
          <p class={styles.description}>This adds a linked chat. The conversation keeps its original computer and working folder.</p>
          <div class={styles.field}>
            <span class={styles.fieldLabel}>Workspace</span>
            <LoadingMenu
              ariaLabel="Workspace"
              value={workspaceId()}
              onChange={selectWorkspace}
              options={workspaces()}
              disabled={!!props.busy}
              emptyLabel="No available workspaces"
              placeholder="Select a workspace…"
              data-testid="native-conversation-workspace-menu"
            />
          </div>
          <div class={styles.field}>
            <span class={styles.fieldLabel}>Worktree</span>
            <DropdownMenu
              as="div"
              aria-label="Worktree"
              class={styles.worktreeMenu}
              matchTriggerWidth
              data-testid="native-conversation-worktree-menu"
              popoverRef={element => worktreeMenu = element}
              onToggle={(open) => {
                if (open)
                  setFilter('')
              }}
              trigger={triggerProps => (
                <button
                  {...triggerProps}
                  type="button"
                  class={styles.worktreeTrigger}
                  aria-label="Worktree"
                  disabled={!!props.busy || worktrees().length === 0}
                >
                  <span class={styles.triggerLabel}>{destination()?.label ?? (worktrees().length ? 'Select a worktree…' : 'No available worktrees')}</span>
                  <Icon icon={ChevronDown} size="sm" />
                </button>
              )}
            >
              <input
                type="search"
                aria-label="Filter worktrees"
                placeholder="Filter worktrees…"
                value={filter()}
                onInput={event => setFilter(event.currentTarget.value)}
              />
              <div role="group" aria-label="Worktrees" class={styles.worktreeOptions}>
                <For each={filteredWorktrees()}>
                  {(item) => {
                    const choice = () => (
                      <DropdownMenuCheckableItem
                        kind="radio"
                        label={`${item.label} · ${item.workerName}`}
                        checked={item.id === destinationId()}
                        disabled={!!props.busy || !!item.disabledReason}
                        detail={() => item.disabledReason ? 'Unavailable' : ''}
                        revealClippedLabel={!item.disabledReason}
                        data-testid={`native-conversation-destination-${item.id}`}
                        onSelect={() => {
                          setDestinationId(item.id)
                          worktreeMenu?.hidePopover()
                        }}
                      />
                    )
                    return (
                      <Show when={item.disabledReason} fallback={choice()}>
                        {reason => <Tooltip text={reason()}>{choice()}</Tooltip>}
                      </Show>
                    )
                  }}
                </For>
                <Show when={filteredWorktrees().length === 0}>
                  <p class={styles.description}>No matching worktrees.</p>
                </Show>
              </div>
            </DropdownMenu>
            <Show when={destination()}>
              {selected => (
                <p class={styles.description}>
                  {selected().workerName}
                  {' · '}
                  {selected().workingDir}
                </p>
              )}
            </Show>
          </div>
          <Show when={props.destinations.length === 0}>
            <p class={styles.description} role="status">Open a worktree in a LeapMux workspace first, then add this chat.</p>
          </Show>
          <Show when={destination()?.disabledReason}>
            {reason => <p class={styles.description} role="status">{reason()}</p>}
          </Show>
          <Show when={destinationId() && !destination()}>
            <p class={styles.description} role="status">The selected worktree is no longer available. Choose another worktree.</p>
          </Show>
          <Show when={props.error}>{error => <p class={styles.error} role="alert">{error()}</p>}</Show>
        </section>
        <footer class={actionsFooter}>
          <button type="button" class="outline" disabled={props.busy} onClick={() => props.onClose()}>Cancel</button>
          <button type="submit" disabled={!!props.busy || !destination() || !!destination()?.disabledReason}>
            {props.busy ? 'Adding…' : 'Add to worktree'}
          </button>
        </footer>
      </form>
    </Dialog>
  )
}
