import ChevronRight from 'lucide-solid/icons/chevron-right'
import { Icon } from '~/components/common/Icon'
import * as styles from './ActivityGroupHeader.css'

export interface ActivityGroupHeaderProps {
  expanded: boolean
  working: boolean
  /** True only when this header replaces the separate live indicator. */
  busyIndicator?: boolean
  count: number
  onToggle: () => void
  premeasureMode?: boolean
}

/** One accessible disclosure for an uninterrupted run of agent activity. */
export function ActivityGroupHeader(props: ActivityGroupHeaderProps) {
  return (
    <button
      type="button"
      class={styles.header}
      data-testid="activity-group-toggle"
      aria-expanded={props.expanded}
      aria-label={`${props.expanded ? 'Collapse' : 'Expand'} activity (${props.count} ${props.count === 1 ? 'message' : 'messages'})`}
      tabIndex={props.premeasureMode ? -1 : 0}
      onClick={() => props.onToggle()}
    >
      <Icon icon={ChevronRight} size="sm" class={props.expanded ? styles.chevronExpanded : styles.chevron} />
      <span class={styles.label} data-testid={(props.busyIndicator ?? (props.working && !props.expanded)) ? 'thinking-indicator' : undefined}>{props.working ? 'Working' : 'Activity'}</span>
      <span class={styles.count}>{props.count}</span>
    </button>
  )
}
