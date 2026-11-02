import type { WorkflowComputer } from '~/stores/workflows'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  WorkflowAgentStatus,
  WorkflowApprovalKind,
  WorkflowApprovalStatus,
  WorkflowRunAction,
  WorkflowRunDetailSchema,
  WorkflowRunStatus,
  WorkflowTaskAction,
  WorkflowTaskOutcome,
  WorkflowTaskStatus,
} from '~/generated/proto/leapmux/v1/workflow_pb'
import { WorkflowRunPanel } from './WorkflowRunPanel'

const rpc = vi.hoisted(() => ({
  getWorkflowRun: vi.fn(),
  decideWorkflowApproval: vi.fn(),
  controlWorkflowRun: vi.fn(),
  controlWorkflowTask: vi.fn(),
  updateWorkflowRole: vi.fn(),
  sendWorkflowAgentMessage: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => rpc)

const computers: WorkflowComputer[] = [
  { id: 'mac', name: 'Mac Studio', online: true, os: 'darwin' },
  { id: 'linux', name: 'Linux box', online: false, os: 'linux' },
]

function detail(status = WorkflowRunStatus.WAITING) {
  return create(WorkflowRunDetailSchema, {
    run: {
      id: 'run-1',
      name: 'Ship it',
      status,
      statusDetail: 'Waiting for your approval',
      objective: 'Ship the feature',
      workingDir: '/repo',
      workspaceId: 'ws-1',
      coordinatorWorkerId: 'mac',
      leadSlotId: 'slot-lead',
      leadAgentId: 'agent-lead',
    },
    roles: [
      { roleKey: 'lead', role: { key: 'lead', name: 'Lead', isLead: true, agent: { agentProvider: AgentProvider.CLAUDE_CODE } }, workerId: 'mac' },
      { roleKey: 'builder', role: { key: 'builder', name: 'Builder', agent: { agentProvider: AgentProvider.CODEX, placement: { strategy: 1, workerId: 'linux' } } }, workerId: 'linux' },
    ],
    agents: [
      { slotId: 'slot-lead', roleKey: 'lead', agentId: 'agent-lead', workerId: 'mac', status: WorkflowAgentStatus.ACTIVE },
      { slotId: 'slot-b', roleKey: 'builder', agentId: 'agent-b', workerId: 'linux', status: WorkflowAgentStatus.UNREACHABLE, statusDetail: 'Computer offline' },
    ],
    tasks: [
      {
        id: 'task-build',
        title: 'Build the app',
        roleKey: 'builder',
        status: WorkflowTaskStatus.SUBMITTED,
        attempt: 1,
        maxAttempts: 3,
        slotId: 'slot-b',
        result: {
          outcome: WorkflowTaskOutcome.COMPLETED,
          summary: 'Built cleanly',
          gitBranch: 'wf/run-1/builder',
          gitCommit: '0123456789abcdef',
          testsRan: true,
          testsPassed: true,
          artifacts: ['build/app.ipa'],
          evidence: [{ label: 'Log', value: '/tmp/build.log' }],
        },
      },
      { id: 'task-test', title: 'Test on device', roleKey: 'builder', status: WorkflowTaskStatus.FAILED, attempt: 3, maxAttempts: 3, dependsOn: ['task-build'] },
    ],
    approvals: [
      { id: 'appr-1', taskId: 'task-build', kind: WorkflowApprovalKind.TASK_COMPLETION, status: WorkflowApprovalStatus.PENDING, title: 'Accept the build' },
    ],
    events: [
      { id: 1n, kind: 1, message: 'Run created', atMs: 1000n },
      { id: 2n, kind: 2, message: 'Task submitted', atMs: 2000n },
    ],
  })
}

function renderPanel(overrides: Partial<Parameters<typeof WorkflowRunPanel>[0]> = {}) {
  const props = {
    runRef: { coordinatorWorkerId: 'mac', runId: 'run-1' },
    computers,
    onOpenAgent: vi.fn(),
    onLeadKnown: vi.fn(),
    onClose: vi.fn(),
    ...overrides,
  }
  render(() => <WorkflowRunPanel {...props} />)
  return props
}

beforeEach(() => {
  vi.clearAllMocks()
  rpc.getWorkflowRun.mockResolvedValue({ detail: detail() })
  rpc.decideWorkflowApproval.mockResolvedValue({ detail: detail(WorkflowRunStatus.RUNNING) })
  rpc.controlWorkflowRun.mockResolvedValue({ detail: detail(WorkflowRunStatus.PAUSED) })
  rpc.controlWorkflowTask.mockResolvedValue({ detail: detail() })
})

afterEach(() => {
  cleanup()
})

describe('WorkflowRunPanel', () => {
  it('renders the run, its agents, tasks with results, approvals and the newest event first', async () => {
    const props = renderPanel()
    expect(await screen.findByRole('heading', { name: 'Ship it' })).toBeInTheDocument()
    expect(rpc.getWorkflowRun).toHaveBeenCalledWith('mac', { runId: 'run-1' })
    expect(screen.getByTestId('workflow-run-status')).toHaveTextContent('Waiting on you')

    const tasks = screen.getAllByTestId('workflow-task')
    expect(tasks).toHaveLength(2)
    const build = within(tasks[0]!)
    expect(build.getByText('Build the app')).toBeInTheDocument()
    expect(build.getByText('In review')).toBeInTheDocument()
    expect(build.getByText('builder · attempt 1/3')).toBeInTheDocument()
    expect(build.getByText('Built cleanly')).toBeInTheDocument()
    expect(build.getByText('wf/run-1/builder @ 0123456789ab')).toBeInTheDocument()
    expect(build.getByText('Tests passed')).toBeInTheDocument()
    expect(build.getByText('build/app.ipa')).toBeInTheDocument()
    // The assignee's computer comes from its slot.
    expect(build.getByTestId('workflow-computer-label')).toHaveTextContent('Linux box')
    const failed = within(tasks[1]!)
    expect(failed.getByText('After: Build the app')).toBeInTheDocument()
    expect(failed.getByRole('button', { name: 'Retry' })).toBeInTheDocument()

    const approval = within(screen.getByTestId('workflow-approval'))
    expect(approval.getByText('Accept the build')).toBeInTheDocument()
    expect(approval.getByText('Task: Build the app')).toBeInTheDocument()

    const agents = screen.getAllByTestId('workflow-agent')
    expect(within(agents[1]!).getByText('Unreachable')).toBeInTheDocument()
    expect(within(agents[1]!).getByText('Computer offline')).toBeInTheDocument()

    const events = within(screen.getByRole('region', { name: 'Activity' })).getAllByRole('listitem')
    expect(events[0]).toHaveTextContent('Task submitted')
    expect(events[1]).toHaveTextContent('Run created')

    // The Lead is announced once, for the shell to focus its tab.
    expect(props.onLeadKnown).toHaveBeenCalledWith({ agentId: 'agent-lead', workerId: 'mac', workspaceId: 'ws-1' })
  })

  it('approves a pending approval with its note', async () => {
    renderPanel()
    const approval = within(await screen.findByTestId('workflow-approval'))
    fireEvent.input(approval.getByRole('textbox'), { target: { value: 'Looks good' } })
    fireEvent.click(approval.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(rpc.decideWorkflowApproval).toHaveBeenCalledWith('mac', {
      runId: 'run-1',
      approvalId: 'appr-1',
      approve: true,
      note: 'Looks good',
    }))
    // The answer is the fresh detail, painted without waiting for a poll.
    await waitFor(() => expect(screen.getByTestId('workflow-run-status')).toHaveTextContent('Running'))
  })

  it('rejects with approve=false', async () => {
    renderPanel()
    const approval = within(await screen.findByTestId('workflow-approval'))
    fireEvent.click(approval.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(rpc.decideWorkflowApproval).toHaveBeenCalledWith('mac', expect.objectContaining({ approve: false })))
  })

  it('pauses the run and retries a failed task', async () => {
    renderPanel()
    fireEvent.click(await screen.findByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(rpc.controlWorkflowRun).toHaveBeenCalledWith('mac', { runId: 'run-1', action: WorkflowRunAction.PAUSE }))
    await waitFor(() => expect(screen.getByTestId('workflow-run-status')).toHaveTextContent('Paused'))

    const failed = within(screen.getAllByTestId('workflow-task')[1]!)
    fireEvent.click(failed.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(rpc.controlWorkflowTask).toHaveBeenCalledWith('mac', {
      runId: 'run-1',
      taskId: 'task-test',
      action: WorkflowTaskAction.RETRY,
      workerId: '',
    }))
  })

  it('opens an agent\'s chat in the run\'s workspace', async () => {
    const props = renderPanel()
    fireEvent.click(await screen.findByRole('button', { name: 'Open chat: Builder' }))
    expect(props.onOpenAgent).toHaveBeenCalledWith({ agentId: 'agent-b', workerId: 'linux', workspaceId: 'ws-1' })
  })

  it('shows an engine refusal beside the controls', async () => {
    rpc.controlWorkflowRun.mockRejectedValueOnce(new Error('not allowed in the current state: only a running or waiting run can be paused'))
    renderPanel()
    fireEvent.click(await screen.findByRole('button', { name: 'Pause' }))
    expect(await screen.findByText('Only a running or waiting run can be paused')).toBeInTheDocument()
  })
})
