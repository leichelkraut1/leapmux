import type { OpenTradingTabDeps } from './openTradingResearchTab'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkerSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { openTradingResearchTab } from './openTradingResearchTab'

const mocks = vi.hoisted(() => ({
  registerTabPayload: vi.fn(),
  revokeTabPayload: vi.fn(),
  getTradingLabSettings: vi.fn(),
  fetchWorkerInfo: vi.fn(),
  openTabInFocusedTile: vi.fn(),
  showWarnToast: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => ({
  registerTabPayload: mocks.registerTabPayload,
  revokeTabPayload: mocks.revokeTabPayload,
  getTradingLabSettings: mocks.getTradingLabSettings,
}))
vi.mock('~/stores/workerInfo.store', () => ({ workerInfoStore: { fetchWorkerInfo: mocks.fetchWorkerInfo } }))
vi.mock('~/components/shell/openTabInFocusedTile', () => ({ openTabInFocusedTile: mocks.openTabInFocusedTile }))
vi.mock('~/components/common/Toast', () => ({ showWarnToast: mocks.showWarnToast }))

const TAB_CAP = 'tab:trading-research'

function deps(workers: { id: string, caps: string[] }[]): OpenTradingTabDeps {
  return {
    workers: workers.map(w => create(WorkerSchema, { id: w.id, online: true })),
    workerInfo: id => ({ capabilities: workers.find(w => w.id === id)?.caps ?? [] }) as never,
    computers: workers.map(w => ({ id: w.id, name: w.id.toUpperCase(), online: true, os: 'windows' })),
    activeWorkspaceId: () => 'ws-1',
    isWorkspaceMutatable: () => true,
    view: { all: () => [] } as never,
    layoutStore: {} as never,
    selection: {} as never,
    metadata: {} as never,
    selectTab: vi.fn(),
    selectWorkspace: vi.fn(),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.registerTabPayload.mockResolvedValue({})
  mocks.revokeTabPayload.mockResolvedValue({})
  mocks.openTabInFocusedTile.mockReturnValue('tile-1')
})

describe('openTradingResearchTab', () => {
  it('refuses with a message when no computer can hold the tab, and places nothing', async () => {
    await openTradingResearchTab(deps([{ id: 'pc', caps: ['os:windows'] }]))
    expect(mocks.registerTabPayload).not.toHaveBeenCalled()
    expect(mocks.openTabInFocusedTile).not.toHaveBeenCalled()
    expect(mocks.showWarnToast).toHaveBeenCalledWith('Cannot open Trading research', expect.objectContaining({ message: expect.stringMatching(/Update the lab computer/) }))
  })

  it('names the computer whose worker is too old when one is asked for', async () => {
    await openTradingResearchTab(deps([{ id: 'mac', caps: [] }]), 'mac')
    expect(mocks.openTabInFocusedTile).not.toHaveBeenCalled()
    expect(mocks.showWarnToast.mock.calls[0]?.[1].message).toMatch(/MAC runs an older LeapMux worker/)
  })

  it('registers with the worker before placing the tab', async () => {
    await openTradingResearchTab(deps([{ id: 'old', caps: [] }, { id: 'lab', caps: [TAB_CAP] }]))
    expect(mocks.registerTabPayload).toHaveBeenCalledWith('lab', expect.objectContaining({ payload: { kind: { case: 'tradingResearch', value: {} } } }))
    expect(mocks.openTabInFocusedTile).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ type: TabType.TRADING_RESEARCH, workerId: 'lab' }), { hydrated: true })
    expect(mocks.registerTabPayload.mock.invocationCallOrder[0]).toBeLessThan(mocks.openTabInFocusedTile.mock.invocationCallOrder[0] ?? 0)
  })

  it('shows the worker refusal instead of a tab that vanishes', async () => {
    mocks.registerTabPayload.mockRejectedValue(new Error('payload specifies no tab kind'))
    await openTradingResearchTab(deps([{ id: 'lab', caps: [TAB_CAP] }]))
    expect(mocks.openTabInFocusedTile).not.toHaveBeenCalled()
    expect(mocks.showWarnToast).toHaveBeenCalledWith('Cannot open Trading research on LAB', expect.any(Error))
  })

  it('prefers the computer serving the trading connector', async () => {
    mocks.getTradingLabSettings.mockImplementation(async (id: string) => ({ settings: { connectorUrl: id === 'luke' ? 'https://lab.example.com/lab-mcp' : '' } }))
    await openTradingResearchTab(deps([{ id: 'pc', caps: [TAB_CAP] }, { id: 'luke', caps: [TAB_CAP] }]))
    expect(mocks.openTabInFocusedTile).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ workerId: 'luke' }), expect.anything())
  })
})
