import type { StartupOptionGroup } from '~/components/chat/providers/capabilities'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { OptionFields } from '~/stores/workflows'
import { createMemo, Show } from 'solid-js'
import { providerFor } from '~/components/chat/providers/registry'
import { AgentStartupOptions } from '~/components/shell/AgentStartupOptions'
import * as styles from './Workflows.css'

function startupGroupsFor(provider: AgentProvider | undefined): readonly StartupOptionGroup[] {
  return provider === undefined ? [] : providerFor(provider)?.configuration?.startupOptionGroups ?? []
}

/**
 * Model, effort and (optionally) permission mode as plain text, plus the
 * provider's static startup axes. No catalog exists before an agent does, so
 * a blank field means "the provider's default on that computer" and no model
 * id is ever suggested from here.
 */
export function OptionFieldsEditor(props: {
  provider: AgentProvider | undefined
  value: OptionFields
  onChange: (next: OptionFields) => void
  showPermissionMode?: boolean
  idPrefix: string
}) {
  const groups = createMemo(() => startupGroupsFor(props.provider))
  // Only the ids the groups own, and only values they offer: the startup
  // resolver refuses anything else, and a stored value from another provider
  // must not take the form down.
  const selected = () => {
    const out: Record<string, string> = {}
    for (const group of groups()) {
      const value = props.value.rest[group.id]
      if (value !== undefined && group.options.some(option => option.value === value))
        out[group.id] = value
    }
    return out
  }
  const set = (patch: Partial<OptionFields>) => props.onChange({ ...props.value, ...patch })
  return (
    <>
      <div class={styles.grid3}>
        <label class={styles.field}>
          Model
          <input
            type="text"
            placeholder="Provider default"
            value={props.value.model}
            onInput={e => set({ model: e.currentTarget.value })}
            data-testid={`${props.idPrefix}-model`}
          />
        </label>
        <label class={styles.field}>
          Effort
          <input
            type="text"
            placeholder="Provider default"
            value={props.value.effort}
            onInput={e => set({ effort: e.currentTarget.value })}
            data-testid={`${props.idPrefix}-effort`}
          />
        </label>
        <Show when={props.showPermissionMode}>
          <label class={styles.field}>
            Permission mode
            <input
              type="text"
              placeholder="Provider default"
              value={props.value.permissionMode}
              onInput={e => set({ permissionMode: e.currentTarget.value })}
            />
          </label>
        </Show>
      </div>
      <Show when={groups().length > 0}>
        <AgentStartupOptions
          groups={groups()}
          selected={selected()}
          onChange={(id, value) => set({ rest: { ...props.value.rest, [id]: value } })}
        />
      </Show>
    </>
  )
}
