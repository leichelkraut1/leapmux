import type { WorkflowComputer } from '~/stores/workflows'
import { createSignal } from 'solid-js'

/**
 * What a Trading research tab needs from the shell: the computers to pick a
 * lab from, and the way to open (or focus) another lab's tab. `WorkflowsHost`
 * owns both and publishes them here, because a tab's pane renders inside the
 * tile tree, far from the host.
 */
const [computers, setComputers] = createSignal<readonly WorkflowComputer[]>([])
let opener: ((labWorkerId?: string) => void) | undefined

export const tradingTab = {
  computers,
  setComputers,
  /** Open the lab's tab, or focus it when the workspace has one. */
  open(labWorkerId?: string): void {
    opener?.(labWorkerId)
  },
  setOpener(fn: ((labWorkerId?: string) => void) | undefined): void {
    opener = fn
  },
}
