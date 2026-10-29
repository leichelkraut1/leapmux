import type { Component } from 'solid-js'
import type { ContextMenuTargetProps } from '~/components/common/DropdownMenu'
import { For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { SubMenu } from '~/components/common/SubMenu'
import { dangerMenuItem } from '~/styles/shared.css'

/**
 * The pop-out / pop-in affordance, when the surface offers one. Declared here in
 * the common layer so this component does not depend on the shell; the
 * tile-level affordance is its subtype -- see `TilePopAction` in
 * ~/components/shell/TileActionsMenu.tsx, which adds the `testId` its own
 * trigger carries.
 */
export interface TabPopAction {
  label: string
  onClick: () => void
}

export interface TabContextMenuProps extends ContextMenuTargetProps {
  /** Start the inline rename. Omit to hide the item. */
  'onRename'?: () => void
  /** Close the tab. Omit to hide the item -- a tab the surface refuses to close has no Close. */
  'onClose'?: () => void
  /** A close is already in flight. The item stays visible, so the row's menu does not reshape mid-click. */
  'isClosing'?: boolean
  'pop'?: TabPopAction
  'move'?: {
    targets: readonly { id: string, label: string }[]
    onSelect: (workspaceId: string) => void
  }
  'data-testid'?: string
}

/**
 * The right-click / long-press menu for a tab, shared by the sidebar's tab leaf
 * and the tile tab bar's tab.
 *
 * The surface supplies the available rename, close, floating-window and
 * workspace destinations. Moving a sidebar tab is an explicit menu choice.
 *
 * Neither tab row gets a kebab. Both already spend their one hover-revealed action
 * slot on the close X, and a second button beside it would widen every row and
 * duplicate Close.
 *
 * Renders nothing when no item would appear. A menu that opens empty is worse than
 * a right-click that falls through to the browser's own.
 */
export const TabContextMenu: Component<TabContextMenuProps> = (props) => {
  const hasMove = () => Boolean(props.move?.targets.length)
  const hasAnyItem = () => Boolean(props.onRename || props.onClose || props.pop || hasMove())

  return (
    <Show when={hasAnyItem()}>
      <DropdownMenu
        {...(props.contextMenuFor !== undefined ? { contextMenuFor: props.contextMenuFor } : {})}
        {...(props['data-testid'] !== undefined ? { 'data-testid': props['data-testid'] } : {})}
      >
        <Show when={props.onRename}>
          <button role="menuitem" data-testid="tab-menu-rename" onClick={() => props.onRename!()}>
            Rename
          </button>
        </Show>

        <Show when={props.pop}>
          {pop => (
            <button role="menuitem" data-testid="tab-menu-pop" onClick={() => pop().onClick()}>
              {pop().label}
            </button>
          )}
        </Show>

        <Show when={hasMove()}>
          <SubMenu label="Move to workspace" data-testid="tab-menu-move" popoverTestId="tab-menu-move-popover">
            <For each={props.move?.targets ?? []}>
              {target => (
                <button role="menuitem" data-testid="tab-menu-move-target" data-workspace-id={target.id} onClick={() => props.move?.onSelect(target.id)}>
                  {target.label}
                </button>
              )}
            </For>
          </SubMenu>
        </Show>

        <Show when={props.onClose}>
          {/* Only when something precedes it, so a Close-only menu has no leading rule. */}
          <Show when={props.onRename || props.pop || hasMove()}>
            <hr />
          </Show>
          <button
            role="menuitem"
            class={dangerMenuItem}
            data-testid="tab-menu-close"
            disabled={props.isClosing}
            onClick={() => {
              if (!props.isClosing)
                props.onClose!()
            }}
          >
            Close
          </button>
        </Show>
      </DropdownMenu>
    </Show>
  )
}
