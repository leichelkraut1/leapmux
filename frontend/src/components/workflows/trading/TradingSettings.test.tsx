import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  GetTradingDashboardResponseSchema,
  TradingBrokerAuth,
  TradingBrokerSignInSchema,
  TradingLabSettingsSchema,
  TradingMcpConfigSchema,
  TradingMcpTransport,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { TradingSettings } from './TradingSettings'

const rpc = vi.hoisted(() => ({
  getTradingLabSettings: vi.fn(),
  getTradingDashboard: vi.fn(),
  saveTradingLabSettings: vi.fn(),
  startTradingBrokerSignIn: vi.fn(),
  signOutTradingBroker: vi.fn(),
  verifyTradingBroker: vi.fn(),
  installTradingSampleData: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => rpc)

function settings(signedIn: boolean, connectorUrl = 'https://lab.example.com/lab-mcp') {
  return create(TradingLabSettingsSchema, {
    connectorUrl,
    robinhood: create(TradingMcpConfigSchema, {
      transport: TradingMcpTransport.HTTP,
      url: 'https://agent.robinhood.com/mcp/trading',
      auth: TradingBrokerAuth.SIGN_IN,
    }),
    brokerSignIn: create(TradingBrokerSignInSchema, { signedIn, signedInAtMs: 1n }),
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  rpc.getTradingDashboard.mockResolvedValue(create(GetTradingDashboardResponseSchema, {}))
  rpc.saveTradingLabSettings.mockImplementation(async (_w: string, req: { settings: unknown }) => ({ settings: req.settings }))
  rpc.startTradingBrokerSignIn.mockResolvedValue({ authorizeUrl: 'https://robinhood.com/oauth?state=s' })
})

afterEach(() => cleanup())

describe('TradingSettings', () => {
  it('saves, then sends the opened tab to the broker sign-in', async () => {
    rpc.getTradingLabSettings.mockResolvedValue({ settings: settings(false) })
    const tab = { opener: {}, location: { href: '' }, close: vi.fn() }
    const open = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    render(() => <TradingSettings workerId="w1" />)

    expect(await screen.findByText('Not signed in')).toBeInTheDocument()
    expect(screen.queryByText(/Confirmation passphrase/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('trading-broker-sign-in-button'))
    expect(open).toHaveBeenCalledWith('', '_blank')
    await waitFor(() => expect(tab.location.href).toBe('https://robinhood.com/oauth?state=s'))
    expect(tab.opener).toBeNull()
    expect(rpc.saveTradingLabSettings).toHaveBeenCalledBefore(rpc.startTradingBrokerSignIn)
    open.mockRestore()
  })

  it('shows the link when the browser blocks the tab', async () => {
    rpc.getTradingLabSettings.mockResolvedValue({ settings: settings(false) })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    render(() => <TradingSettings workerId="w1" />)
    fireEvent.click(await screen.findByTestId('trading-broker-sign-in-button'))
    const link = await screen.findByText('Open the Robinhood sign-in')
    expect(link).toHaveAttribute('href', 'https://robinhood.com/oauth?state=s')
    open.mockRestore()
  })

  it('needs the connector address to sign in, and offers sign-out once signed in', async () => {
    rpc.getTradingLabSettings.mockResolvedValue({ settings: settings(false, '') })
    render(() => <TradingSettings workerId="w1" />)
    expect(await screen.findByTestId('trading-broker-sign-in-button')).toBeDisabled()
    cleanup()

    rpc.getTradingLabSettings.mockResolvedValue({ settings: settings(true) })
    rpc.signOutTradingBroker.mockResolvedValue({ settings: settings(false) })
    render(() => <TradingSettings workerId="w1" />)
    expect(await screen.findByText('Signed in')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Sign out'))
    await waitFor(() => expect(rpc.signOutTradingBroker).toHaveBeenCalledWith('w1', {}))
    expect(await screen.findByText('Not signed in')).toBeInTheDocument()
  })
})
