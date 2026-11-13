import type { WorkflowComputer } from '~/stores/workflows'
import { createEffect, createSignal, Show } from 'solid-js'
import { Alert } from '~/components/common/Alert'
import { PillGroup } from '~/components/common/PillGroup'
import { defaultComputerId } from '~/stores/workflows'
import { ComputerPicker } from '../ComputerPicker'
import * as styles from '../Workflows.css'
import { TradingOrders } from './TradingOrders'
import { TradingPortfolio } from './TradingPortfolio'
import { TradingRuns } from './TradingRuns'
import { TradingSettings } from './TradingSettings'

export type TradingSection = 'runs' | 'orders' | 'portfolio' | 'settings'

const SECTIONS = [
  { key: 'runs', label: 'Research runs' },
  { key: 'orders', label: 'Queue and orders' },
  { key: 'portfolio', label: 'Portfolios' },
  { key: 'settings', label: 'Settings' },
] as const

export interface TradingResearchProps {
  computers: readonly WorkflowComputer[]
}

/**
 * The Trading Research view of the blind-trading-lab workflow. Everything it
 * shows lives on one computer: the coordinator of the lab's runs, which holds
 * the snapshots, the frozen predictions, the books and the orders. The
 * template, its roles, models and computers, and the run controls are the
 * ordinary workflow surfaces (Templates and Runs).
 */
export function TradingResearch(props: TradingResearchProps) {
  const [labWorkerId, setLabWorkerId] = createSignal('')
  const [section, setSection] = createSignal<TradingSection>('runs')
  createEffect(() => {
    const ids = props.computers.map(c => c.id)
    if (!labWorkerId() || !ids.includes(labWorkerId()))
      setLabWorkerId(defaultComputerId(props.computers))
  })
  const computer = () => props.computers.find(c => c.id === labWorkerId())

  return (
    <div class={styles.form} data-testid="trading-research">
      <div class={styles.toolbar}>
        <PillGroup<TradingSection>
          label="Trading research section"
          options={SECTIONS}
          selectedKey={section()}
          onSelect={setSection}
          small
        />
        <div class={styles.toolbarField}>
          <span>Lab computer</span>
          <ComputerPicker label="Lab computer" computers={props.computers} value={labWorkerId()} onChange={setLabWorkerId} testId="trading-lab-computer" />
        </div>
      </div>
      <Alert>
        Research, scoring and the shadow portfolios never trade. A real order happens only when you confirm that exact order with your passphrase, or through an execution policy you approved for one position.
      </Alert>
      <Show when={computer() && !computer()?.online}>
        <Alert variant="warning">This computer is offline, so its lab cannot be read.</Alert>
      </Show>
      <Show when={labWorkerId()} keyed>
        {workerId => (
          <>
            <Show when={section() === 'runs'}>
              <TradingRuns workerId={workerId} />
            </Show>
            <Show when={section() === 'orders'}>
              <TradingOrders workerId={workerId} />
            </Show>
            <Show when={section() === 'portfolio'}>
              <TradingPortfolio workerId={workerId} />
            </Show>
            <Show when={section() === 'settings'}>
              <TradingSettings workerId={workerId} />
            </Show>
          </>
        )}
      </Show>
    </div>
  )
}
