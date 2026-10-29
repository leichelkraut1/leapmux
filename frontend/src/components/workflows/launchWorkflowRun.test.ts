import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { WorkflowRunAction, WorkflowTemplateSchema } from '~/generated/proto/leapmux/v1/workflow_pb'
import { launchWorkflowRun } from './launchWorkflowRun'

const calls: string[] = []
const rpc = vi.hoisted(() => ({
  createWorkflowRun: vi.fn(),
  openAgent: vi.fn(),
  attachWorkflowLead: vi.fn(),
  controlWorkflowRun: vi.fn(),
}))

vi.mock('~/api/workerRpc', () => rpc)

const binding = { runId: 'run-1', slotId: 'slot-lead', roleKey: 'lead', coordinatorWorkerId: 'mac', token: 'secret' }

beforeEach(() => {
  calls.length = 0
  vi.clearAllMocks()
  rpc.createWorkflowRun.mockImplementation(async () => {
    calls.push('create')
    return {
      run: { id: 'run-1', name: 'Ship it', workingDir: '/repo' },
      leadBinding: binding,
      leadRole: { key: 'lead', agent: { agentProvider: AgentProvider.CLAUDE_CODE, options: { model: 'opus' }, workingDir: '' } },
    }
  })
  rpc.openAgent.mockImplementation(async () => {
    calls.push('open')
    return { agent: create(AgentInfoSchema, { id: 'agent-lead', workerId: 'mac' }) }
  })
  rpc.attachWorkflowLead.mockImplementation(async () => {
    calls.push('attach')
    return {}
  })
  rpc.controlWorkflowRun.mockImplementation(async () => {
    calls.push('delete')
    return {}
  })
})

function input(placeLeadTab = vi.fn(() => {
  calls.push('tab')
  return true
})) {
  return {
    coordinatorWorkerId: 'mac',
    template: create(WorkflowTemplateSchema, { id: 't', name: 'Ship it' }),
    profiles: [],
    overrides: [{ roleKey: 'tester', placement: { workerId: 'laptop' } }],
    name: ' Ship it ',
    objective: ' Make it work ',
    projectDir: '/repo',
    baseBranch: 'main',
    workspaceId: 'ws-1',
    placeLeadTab,
  }
}

describe('launchWorkflowRun', () => {
  it('creates the run, opens the Lead with its binding, places its tab, then attaches it', async () => {
    const placeLeadTab = vi.fn(() => {
      calls.push('tab')
      return true
    })
    const launched = await launchWorkflowRun(input(placeLeadTab))

    expect(calls).toEqual(['create', 'open', 'tab', 'attach'])
    expect(rpc.createWorkflowRun).toHaveBeenCalledWith('mac', expect.objectContaining({
      name: 'Ship it',
      objective: 'Make it work',
      workingDir: '/repo',
      baseBranch: 'main',
      workspaceId: 'ws-1',
      overrides: [{ roleKey: 'tester', placement: { workerId: 'laptop' } }],
    }))
    expect(rpc.openAgent).toHaveBeenCalledWith('mac', expect.objectContaining({
      agentProvider: AgentProvider.CLAUDE_CODE,
      title: 'Ship it: Lead',
      workerId: 'mac',
      // The Lead has no directory of its own, so it works in the project.
      workingDir: '/repo',
      workflow: binding,
      options: expect.objectContaining({ model: 'opus' }),
    }))
    expect(placeLeadTab).toHaveBeenCalledWith(expect.objectContaining({ id: 'agent-lead' }))
    expect(rpc.attachWorkflowLead).toHaveBeenCalledWith('mac', { runId: 'run-1', agentId: 'agent-lead' })
    expect(launched).toEqual({ coordinatorWorkerId: 'mac', runId: 'run-1', leadAgentId: 'agent-lead', leadTabPlaced: true })
  })

  it('deletes the pending run when the Lead cannot be opened', async () => {
    rpc.openAgent.mockRejectedValueOnce(new Error('provider missing'))
    await expect(launchWorkflowRun(input())).rejects.toThrow('provider missing')
    expect(rpc.controlWorkflowRun).toHaveBeenCalledWith('mac', { runId: 'run-1', action: WorkflowRunAction.DELETE })
    expect(rpc.attachWorkflowLead).not.toHaveBeenCalled()
  })

  it('still attaches the Lead when its tab cannot be placed', async () => {
    const launched = await launchWorkflowRun(input(vi.fn(() => false)))
    expect(rpc.attachWorkflowLead).toHaveBeenCalled()
    expect(launched.leadTabPlaced).toBe(false)
  })
})
