import type { WorkflowComputer } from '~/stores/workflows'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentProfileSchema, WorkerPlacementStrategy, WorkflowTemplateSchema } from '~/generated/proto/leapmux/v1/workflow_pb'
import { WorkflowLaunchForm } from './WorkflowLaunchForm'

const calls: string[] = []
const rpc = vi.hoisted(() => ({
  createWorkflowRun: vi.fn(),
  openAgent: vi.fn(),
  attachWorkflowLead: vi.fn(),
  controlWorkflowRun: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => rpc)

const computers: WorkflowComputer[] = [
  { id: 'mac', name: 'Mac Studio', online: true, os: 'darwin' },
  { id: 'linux', name: 'Linux box', online: true, os: 'linux' },
  { id: 'iphone-mac', name: 'Test Mac', online: false, os: 'darwin' },
]

const profiles = [
  create(AgentProfileSchema, { id: 'p-lead', name: 'Lead', agentProvider: AgentProvider.CLAUDE_CODE }),
  create(AgentProfileSchema, {
    id: 'p-tester',
    name: 'Tester',
    agentProvider: AgentProvider.CODEX,
    placement: { strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'iphone-mac' },
  }),
]

const template = create(WorkflowTemplateSchema, {
  id: 't-1',
  name: 'iOS flow',
  roles: [
    { key: 'lead', name: 'Lead', profileId: 'p-lead', isLead: true },
    { key: 'tester', name: 'Tester', profileId: 'p-tester' },
  ],
})

const binding = { runId: 'run-1', slotId: 'slot-lead', roleKey: 'lead', coordinatorWorkerId: 'mac', token: 'tok' }

beforeEach(() => {
  calls.length = 0
  vi.clearAllMocks()
  rpc.createWorkflowRun.mockImplementation(async () => {
    calls.push('create')
    return {
      run: { id: 'run-1', name: 'iOS flow', workingDir: '/Users/me' },
      leadBinding: binding,
      leadRole: { key: 'lead', agent: { agentProvider: AgentProvider.CLAUDE_CODE, options: {}, workingDir: '' } },
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
})

afterEach(() => {
  cleanup()
})

function renderForm() {
  const onLaunched = vi.fn()
  const placeLeadTab = vi.fn(() => {
    calls.push('tab')
    return true
  })
  render(() => (
    <WorkflowLaunchForm
      template={template}
      profiles={profiles}
      computers={computers}
      workspaceId="ws-1"
      homeDirOf={id => (id === 'mac' ? '/Users/me' : '')}
      placeLeadTab={placeLeadTab}
      onLaunched={onLaunched}
      onCancel={() => {}}
    />
  ))
  return { onLaunched, placeLeadTab }
}

describe('WorkflowLaunchForm', () => {
  it('warns about a pinned role whose computer is offline and keeps it pinned', async () => {
    renderForm()
    const tester = screen.getAllByTestId('workflow-launch-role').find(row => row.dataset.roleKey === 'tester')!
    expect(within(tester).getByTestId('workflow-launch-offline-warning'))
      .toHaveTextContent('Test Mac is offline. This role waits for it; no other computer is used.')
    // The picker still says the template's placement is kept: nothing was substituted.
    expect(within(tester).getByTestId('workflow-launch-role-computer-tester')).toHaveTextContent('Keep the template\'s placement')
    expect(within(tester).getByText(/On Test Mac/)).toBeInTheDocument()

    fireEvent.input(screen.getByTestId('workflow-launch-objective'), { target: { value: 'Ship the app' } })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))
    await waitFor(() => expect(rpc.attachWorkflowLead).toHaveBeenCalled())
    // No override moves the tester anywhere else.
    expect(rpc.createWorkflowRun.mock.calls[0]?.[1]).toMatchObject({ overrides: [] })
  })

  it('launches on the Lead computer: create, open the Lead with its binding, place its tab, attach', async () => {
    const { onLaunched, placeLeadTab } = renderForm()
    // The project directory defaults to the coordinator's home.
    expect(screen.getByTestId('workflow-launch-project-dir')).toHaveValue('/Users/me')
    fireEvent.input(screen.getByTestId('workflow-launch-objective'), { target: { value: 'Ship the app' } })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() => expect(onLaunched).toHaveBeenCalledWith({ coordinatorWorkerId: 'mac', runId: 'run-1', leadAgentId: 'agent-lead', leadTabPlaced: true }))
    expect(calls).toEqual(['create', 'open', 'tab', 'attach'])
    expect(rpc.createWorkflowRun).toHaveBeenCalledWith('mac', expect.objectContaining({
      objective: 'Ship the app',
      workingDir: '/Users/me',
      workspaceId: 'ws-1',
      profiles: profiles.map(p => expect.objectContaining({ id: p.id })),
    }))
    expect(rpc.openAgent).toHaveBeenCalledWith('mac', expect.objectContaining({ workflow: binding, title: 'iOS flow: Lead' }))
    expect(placeLeadTab).toHaveBeenCalledTimes(1)
  })

  it('refuses to launch without an objective or a workspace', () => {
    render(() => (
      <WorkflowLaunchForm
        template={template}
        profiles={profiles}
        computers={computers}
        workspaceId={undefined}
        homeDirOf={() => '/Users/me'}
        placeLeadTab={() => true}
        onLaunched={() => {}}
        onCancel={() => {}}
      />
    ))
    expect(screen.getByRole('button', { name: 'Launch' })).toBeDisabled()
    expect(screen.getByTestId('workflow-launch-refusal')).toHaveTextContent('Open a workspace first')
  })
})
