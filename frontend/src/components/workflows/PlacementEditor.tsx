import type { PlacementDraft, WorkflowComputer } from '~/stores/workflows'
import X from 'lucide-solid/icons/x'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { PillGroup } from '~/components/common/PillGroup'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkerPlacementStrategy } from '~/generated/proto/leapmux/v1/workflow_pb'
import { normalizeCapabilities, offlinePinnedComputer, parseCapabilityInput } from '~/stores/workflows'
import { warningText } from '~/styles/shared.css'
import { ComputerMultiPicker, ComputerPicker } from './ComputerPicker'
import * as styles from './Workflows.css'

const STRATEGIES = [
  { key: WorkerPlacementStrategy.SPECIFIC_WORKER, label: 'Specific computer' },
  { key: WorkerPlacementStrategy.CAPABILITY, label: 'Capability' },
  { key: WorkerPlacementStrategy.ANY_WORKER, label: 'Any' },
] as const

/**
 * Where a profile's or a role's agents run: one computer, any computer with
 * the listed capabilities, or any computer -- optionally only among an
 * allow-list.
 */
export function PlacementEditor(props: {
  value: PlacementDraft
  onChange: (next: PlacementDraft) => void
  computers: readonly WorkflowComputer[]
  idPrefix: string
}) {
  const [capabilityText, setCapabilityText] = createSignal('')
  const set = (patch: Partial<PlacementDraft>) => props.onChange({ ...props.value, ...patch })
  const known = createMemo(() => normalizeCapabilities(props.computers.flatMap(c => [...(c.capabilities ?? [])]))
    .filter(c => !props.value.requiredCapabilities.includes(c)))
  const addCapabilities = () => {
    const added = parseCapabilityInput(capabilityText())
    if (added.length === 0)
      return
    set({ requiredCapabilities: normalizeCapabilities([...props.value.requiredCapabilities, ...added]) })
    setCapabilityText('')
  }
  const offline = () => offlinePinnedComputer(props.value, props.computers)

  return (
    <div class={styles.form}>
      <PillGroup<WorkerPlacementStrategy>
        label="Placement"
        options={STRATEGIES}
        selectedKey={props.value.strategy}
        onSelect={strategy => set({ strategy })}
        small
      />
      <Show when={props.value.strategy === WorkerPlacementStrategy.SPECIFIC_WORKER}>
        <div class={styles.field}>
          <span>Computer</span>
          <ComputerPicker
            label="Computer"
            computers={props.computers}
            value={props.value.workerId}
            onChange={workerId => set({ workerId })}
            testId={`${props.idPrefix}-placement-computer`}
          />
          <Show when={offline()}>
            <span class={warningText} role="status">
              This computer is offline. Agents pinned to it wait until it comes back; no other computer is used.
            </span>
          </Show>
        </div>
      </Show>
      <Show when={props.value.strategy === WorkerPlacementStrategy.CAPABILITY}>
        <div class={styles.field}>
          <span>Required capabilities</span>
          <div class={styles.chips}>
            <For each={props.value.requiredCapabilities}>
              {capability => (
                <span class={`badge outline ${styles.chip}`}>
                  {capability}
                  <Tooltip text={`Remove ${capability}`} ariaLabel>
                    <button
                      type="button"
                      class="ghost small"
                      onClick={() => set({ requiredCapabilities: props.value.requiredCapabilities.filter(c => c !== capability) })}
                    >
                      <Icon icon={X} size="xs" />
                    </button>
                  </Tooltip>
                </span>
              )}
            </For>
          </div>
          <div class={styles.inline}>
            <input
              type="text"
              aria-label="Add capabilities"
              placeholder="os:darwin, xcode, gpu"
              value={capabilityText()}
              onInput={e => setCapabilityText(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addCapabilities()
                }
              }}
            />
            <button type="button" class="outline small" onClick={addCapabilities}>Add</button>
          </div>
          <Show when={known().length > 0}>
            <div class={styles.chips}>
              <span class={styles.hint}>Known:</span>
              <For each={known()}>
                {capability => (
                  <button
                    type="button"
                    class="ghost small"
                    onClick={() => set({ requiredCapabilities: normalizeCapabilities([...props.value.requiredCapabilities, capability]) })}
                  >
                    {`+ ${capability}`}
                  </button>
                )}
              </For>
            </div>
          </Show>
        </div>
      </Show>
      <Show when={props.value.strategy !== WorkerPlacementStrategy.SPECIFIC_WORKER}>
        <div class={styles.field}>
          <span>Allowed computers</span>
          <ComputerMultiPicker
            label="Allowed computers"
            computers={props.computers}
            value={props.value.allowedWorkerIds}
            onChange={allowedWorkerIds => set({ allowedWorkerIds })}
            emptyLabel="Every computer"
          />
        </div>
      </Show>
    </div>
  )
}
