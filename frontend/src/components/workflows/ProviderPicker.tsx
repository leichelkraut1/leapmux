import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, For, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { getAvailableAgentProviders, sortAgentProvidersByName } from '~/lib/agentProviders'
import { fieldTriggerChevron } from '~/styles/shared.css'
import * as styles from './Workflows.css'

/**
 * A provider choice for a template role, whose first row INHERITS the
 * profile's provider (`AgentProvider.UNSPECIFIED`). A profile itself names its
 * provider through `AgentProviderSelector`.
 */
export function ProviderPicker(props: {
  label: string
  value: AgentProvider
  onChange: (provider: AgentProvider) => void
  inheritLabel: string
  availableProviders?: readonly AgentProvider[]
  testId?: string
}) {
  const providers = createMemo(() => sortAgentProvidersByName(getAvailableAgentProviders(props.availableProviders)))
  return (
    <DropdownMenu
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
          data-testid={props.testId}
        >
          <span class={styles.triggerValue}>
            <Show when={props.value !== AgentProvider.UNSPECIFIED} fallback={<span>{props.inheritLabel}</span>}>
              <AgentProviderIcon provider={props.value} size={16} />
              <span>{agentProviderLabel(props.value)}</span>
            </Show>
          </span>
          <ChevronDown size={16} class={fieldTriggerChevron} />
        </button>
      )}
    >
      <DropdownMenuCheckableItem kind="radio" label={props.inheritLabel} checked={props.value === AgentProvider.UNSPECIFIED} onSelect={() => props.onChange(AgentProvider.UNSPECIFIED)} />
      <For each={providers()}>
        {provider => (
          <DropdownMenuCheckableItem
            kind="radio"
            label={agentProviderLabel(provider)}
            checked={provider === props.value}
            leading={<AgentProviderIcon provider={provider} size={16} />}
            onSelect={() => props.onChange(provider)}
          />
        )}
      </For>
    </DropdownMenu>
  )
}
