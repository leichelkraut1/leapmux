import type { Component } from 'solid-js'
import type { Tab } from '~/stores/tab.types'
import Link from 'lucide-solid/icons/link'
import { Show } from 'solid-js'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { Icon } from './Icon'
import * as styles from './LinkedChatBadge.css'
import { Tooltip } from './Tooltip'

/** Distinguish a saved outside-chat reference from a LeapMux-owned agent. */
export const LinkedChatBadge: Component<{ tab: Tab }> = props => (
  <Show when={props.tab.type === TabType.NATIVE_CONVERSATION ? props.tab : false}>
    {(tab) => {
      const sourceName = () => {
        const workerId = tab().sourceWorkerId
        return workerId ? workerInfoStore.workerInfo(workerId)?.name : undefined
      }
      const explanation = () => {
        const name = sourceName()
        return name
          ? `Linked chat. This conversation stays in its original app on ${name}.`
          : 'Linked chat. This conversation stays in its original app and on its source computer.'
      }
      return (
        <Tooltip text={explanation()}>
          <span class={styles.badge} data-testid="linked-chat-badge">
            <Icon icon={Link} size="xs" aria-hidden="true" />
            Linked
          </span>
        </Tooltip>
      )
    }}
  </Show>
)
