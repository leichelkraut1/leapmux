import * as t from './Trading.css'
import { TradingResearch } from './TradingResearch'
import { tradingTab } from './tradingTab'

/** A Trading research tab's pane: the lab of the tab's worker. */
export function TradingResearchTab(props: { workerId: string }) {
  return (
    <div class={t.tabPane} data-testid="trading-research-tab">
      <TradingResearch
        computers={tradingTab.computers()}
        labWorkerId={props.workerId}
        onLabChange={id => tradingTab.open(id)}
      />
    </div>
  )
}
