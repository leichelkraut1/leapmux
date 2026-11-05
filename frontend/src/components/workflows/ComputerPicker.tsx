import type { WorkflowComputer, WorkflowTone } from '~/stores/workflows'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { For, Show } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { StatusDot } from '~/components/common/StatusDot'
import { computerDescription, computerName, toneBadgeVariant } from '~/stores/workflows'
import { fieldTriggerChevron } from '~/styles/shared.css'
import * as styles from './Workflows.css'

/** The online dot every computer row carries. Its label states the state. */
export function ComputerDot(props: { online: boolean }) {
  return (
    <StatusDot
      class={props.online ? styles.dotOnline : styles.dotOffline}
      label={props.online ? 'Online' : 'Offline'}
      status={props.online ? 'online' : 'offline'}
    />
  )
}

/** A computer's name with its online dot and OS, for a row in a list. */
export function ComputerLabel(props: { computers: readonly WorkflowComputer[], workerId: string }) {
  const computer = () => props.computers.find(c => c.id === props.workerId)
  return (
    <span class={styles.inline} data-testid="workflow-computer-label">
      <Show when={computer()} fallback={<span>{props.workerId ? computerName(props.computers, props.workerId) : 'No computer'}</span>}>
        {c => (
          <>
            <ComputerDot online={c().online} />
            <span>{c().name}</span>
            <Show when={c().os}>
              <span class={styles.secondary}>{c().os}</span>
            </Show>
          </>
        )}
      </Show>
    </span>
  )
}

export function StatusBadge(props: { label: string, tone: WorkflowTone, testId?: string }) {
  return (
    <span class="badge" data-variant={toneBadgeVariant(props.tone)} data-testid={props.testId}>
      {props.label}
    </span>
  )
}

/**
 * Picks one computer. Each row shows its name, an online dot and its OS, so
 * an offline computer is chosen knowingly rather than hidden: a role pinned to
 * it waits for it.
 */
export function ComputerPicker(props: {
  label: string
  computers: readonly WorkflowComputer[]
  value: string
  onChange: (workerId: string) => void
  /** A first row meaning "no computer chosen", e.g. "Keep the template's placement". */
  emptyLabel?: string
  disabled?: boolean
  testId?: string
}) {
  const selected = () => props.computers.find(c => c.id === props.value)
  return (
    <DropdownMenu
      class={styles.menu}
      aria-label={props.label}
      matchTriggerWidth
      {...(props.testId !== undefined ? { 'data-testid': `${props.testId}-menu` } : {})}
      trigger={trigger => (
        <button
          type="button"
          class={styles.trigger}
          aria-label={props.label}
          aria-expanded={trigger['aria-expanded']}
          ref={trigger.ref}
          onPointerDown={trigger.onPointerDown}
          onClick={trigger.onClick}
          disabled={props.disabled}
          data-testid={props.testId}
        >
          <span class={styles.triggerValue}>
            <Show
              when={selected()}
              fallback={<span>{props.value ? computerName(props.computers, props.value) : (props.emptyLabel ?? 'Choose a computer')}</span>}
            >
              {c => (
                <>
                  <ComputerDot online={c().online} />
                  <span>{c().name}</span>
                  <span class={styles.secondary}>{computerDescription(c())}</span>
                </>
              )}
            </Show>
          </span>
          <ChevronDown size={16} class={fieldTriggerChevron} />
        </button>
      )}
    >
      <Show when={props.emptyLabel}>
        {label => (
          <DropdownMenuCheckableItem kind="radio" label={label()} checked={props.value === ''} onSelect={() => props.onChange('')} />
        )}
      </Show>
      <For each={props.computers}>
        {computer => (
          <DropdownMenuCheckableItem
            kind="radio"
            label={computer.name}
            checked={computer.id === props.value}
            leading={<ComputerDot online={computer.online} />}
            detail={() => computerDescription(computer)}
            data-testid={`workflow-computer-option-${computer.id}`}
            onSelect={() => props.onChange(computer.id)}
          />
        )}
      </For>
    </DropdownMenu>
  )
}

/** Picks any number of computers (an allow-list). The menu stays open while ticking. */
export function ComputerMultiPicker(props: {
  label: string
  computers: readonly WorkflowComputer[]
  value: readonly string[]
  onChange: (workerIds: string[]) => void
  emptyLabel: string
}) {
  const toggle = (id: string) => {
    const next = props.value.includes(id) ? props.value.filter(v => v !== id) : [...props.value, id]
    props.onChange(next)
  }
  const summary = () => props.value.length === 0
    ? props.emptyLabel
    : props.value.map(id => computerName(props.computers, id)).join(', ')
  return (
    <DropdownMenu
      as="div"
      class={styles.menu}
      aria-label={props.label}
      matchTriggerWidth
      trigger={trigger => (
        <button
          type="button"
          class={styles.trigger}
          aria-label={props.label}
          aria-expanded={trigger['aria-expanded']}
          ref={trigger.ref}
          onPointerDown={trigger.onPointerDown}
          onClick={trigger.onClick}
        >
          <span class={styles.triggerValue}>{summary()}</span>
          <ChevronDown size={16} class={fieldTriggerChevron} />
        </button>
      )}
    >
      <For each={props.computers}>
        {computer => (
          <DropdownMenuCheckableItem
            kind="checkbox"
            label={computer.name}
            checked={props.value.includes(computer.id)}
            leading={<ComputerDot online={computer.online} />}
            detail={() => computerDescription(computer)}
            onSelect={() => toggle(computer.id)}
          />
        )}
      </For>
    </DropdownMenu>
  )
}
