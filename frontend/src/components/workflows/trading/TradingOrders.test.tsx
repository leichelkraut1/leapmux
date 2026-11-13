import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  GetTradingDashboardResponseSchema,
  TradingBook,
  TradingCandidateSchema,
  TradingCandidateStatus,
  TradingLabMode,
  TradingLabSettingsSchema,
  TradingOrderSchema,
  TradingOrderSide,
  TradingOrderState,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { TradingOrders } from './TradingOrders'

const rpc = vi.hoisted(() => ({
  getTradingDashboard: vi.fn(),
  prepareTradingOrder: vi.fn(),
  confirmTradingOrder: vi.fn(),
  cancelTradingOrder: vi.fn(),
  decideTradingCandidate: vi.fn(),
  reconcileTradingOrders: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => rpc)

const candidate = create(TradingCandidateSchema, {
  id: 'cand-1',
  source: TradingBook.BLIND,
  status: TradingCandidateStatus.APPROVED,
  strategy: 'long_call',
  occSymbol: 'SYNA  261106C00100000',
  entryLimit: 1.25,
  maxLossPerContract: 125.03,
  evaluationScore: 66,
})

const prepared = create(TradingOrderSchema, {
  id: 'order-1',
  candidateId: 'cand-1',
  state: TradingOrderState.PREPARED,
  side: TradingOrderSide.BUY_TO_OPEN,
  occSymbol: candidate.occSymbol,
  quantity: 1,
  limitPrice: 1.25,
  timeInForce: 'gfd',
  accountId: '5QR12345',
  estimatedFees: 0.06,
  maxLoss: 125.06,
  bid: 1.2,
  ask: 1.25,
  brokerAlerts: ['Options can expire worthless.'],
  checks: ['account may trade', 'quote fresh'],
  fingerprint: 'f'.repeat(64),
  confirmByMs: BigInt(Date.now() + 120_000),
})

function dashboard(mode: TradingLabMode) {
  return create(GetTradingDashboardResponseSchema, {
    settings: create(TradingLabSettingsSchema, { mode }),
    queue: [candidate],
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  rpc.prepareTradingOrder.mockResolvedValue({ order: prepared })
  rpc.confirmTradingOrder.mockResolvedValue({ order: create(TradingOrderSchema, { ...prepared, state: TradingOrderState.SUBMITTED }) })
})

afterEach(() => cleanup())

describe('TradingOrders', () => {
  it('offers no live order in paper mode', async () => {
    rpc.getTradingDashboard.mockResolvedValue(dashboard(TradingLabMode.PAPER))
    render(() => <TradingOrders workerId="w1" />)
    const prepare = await screen.findByTestId('trading-prepare-order')
    expect(prepare).toBeDisabled()
    expect(screen.getByText(/Paper mode/)).toBeInTheDocument()
  })

  it('submits only the exact order a person acknowledged with the passphrase', async () => {
    rpc.getTradingDashboard.mockResolvedValue(dashboard(TradingLabMode.LIVE))
    render(() => <TradingOrders workerId="w1" />)
    fireEvent.click(await screen.findByTestId('trading-prepare-order'))
    await screen.findByTestId('trading-order-ticket')
    expect(rpc.prepareTradingOrder).toHaveBeenCalledWith('w1', { candidateId: 'cand-1', quantity: 1, limitPrice: 1.25 })
    expect(screen.getByText('Options can expire worthless.')).toBeInTheDocument()
    expect(screen.getByText('$125.06')).toBeInTheDocument()

    const confirm = screen.getByTestId('trading-confirm-order')
    expect(confirm).toBeDisabled()
    fireEvent.click(screen.getByTestId('trading-order-ack'))
    expect(confirm).toBeDisabled()
    fireEvent.input(screen.getByTestId('trading-order-passphrase'), { target: { value: 'correct horse battery' } })
    expect(confirm).toBeEnabled()
    expect(rpc.confirmTradingOrder).not.toHaveBeenCalled()

    fireEvent.click(confirm)
    await waitFor(() => expect(rpc.confirmTradingOrder).toHaveBeenCalledTimes(1))
    expect(rpc.confirmTradingOrder).toHaveBeenCalledWith('w1', { orderId: 'order-1', fingerprint: 'f'.repeat(64), passphrase: 'correct horse battery' })
  })
})
