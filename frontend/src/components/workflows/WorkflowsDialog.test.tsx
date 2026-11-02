import type { WorkflowsTab } from './WorkflowsDialog'
import type { WorkflowComputer } from '~/stores/workflows'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentProfileSchema, WorkflowTemplateSchema } from '~/generated/proto/leapmux/v1/workflow_pb'
import { WorkflowsDialog, workflowsTabFromParam } from './WorkflowsDialog'

const rpc = vi.hoisted(() => ({
  listWorkflowLibrary: vi.fn(),
  listWorkflowRuns: vi.fn(),
  installExampleWorkflow: vi.fn(),
  saveWorkflowTemplate: vi.fn(),
  saveAgentProfile: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => rpc)

const dialogPrototype = HTMLDialogElement.prototype
const originalShowModal = Object.getOwnPropertyDescriptor(dialogPrototype, 'showModal')
const originalClose = Object.getOwnPropertyDescriptor(dialogPrototype, 'close')

beforeAll(() => {
  Object.defineProperties(dialogPrototype, {
    showModal: { configurable: true, writable: true, value(this: HTMLDialogElement) { this.setAttribute('open', '') } },
    close: { configurable: true, writable: true, value(this: HTMLDialogElement) { this.removeAttribute('open') } },
  })
})

afterAll(() => {
  if (originalShowModal)
    Object.defineProperty(dialogPrototype, 'showModal', originalShowModal)
  else
    Reflect.deleteProperty(dialogPrototype, 'showModal')
  if (originalClose)
    Object.defineProperty(dialogPrototype, 'close', originalClose)
  else
    Reflect.deleteProperty(dialogPrototype, 'close')
})

const computers: WorkflowComputer[] = [
  { id: 'off', name: 'Old laptop', online: false, os: 'linux' },
  { id: 'mac', name: 'Mac Studio', online: true, os: 'darwin' },
]

const profile = create(AgentProfileSchema, { id: 'p-1', name: 'Builder', agentProvider: AgentProvider.CLAUDE_CODE })
const template = create(WorkflowTemplateSchema, {
  id: 't-1',
  name: 'Ship it',
  version: 2,
  roles: [{ key: 'lead', name: 'Lead', profileId: 'p-1', isLead: true }],
})

beforeEach(() => {
  vi.clearAllMocks()
  rpc.listWorkflowLibrary.mockResolvedValue({ profiles: [profile], templates: [template] })
  rpc.listWorkflowRuns.mockResolvedValue({ runs: [] })
  rpc.installExampleWorkflow.mockResolvedValue({ template: { name: 'Develop, audit and test on iOS' }, profiles: [] })
})

afterEach(() => {
  cleanup()
})

function renderDialog(initial: WorkflowsTab = 'templates') {
  const [tab, setTab] = createSignal<WorkflowsTab>(initial)
  render(() => (
    <WorkflowsDialog
      tab={tab()}
      onTabChange={setTab}
      computers={computers}
      workspaceId="ws-1"
      homeDirOf={() => '/home'}
      placeLeadTab={() => true}
      onLaunched={() => {}}
      onOpenRun={() => {}}
      onClose={() => {}}
    />
  ))
  return { setTab }
}

describe('workflowsTabFromParam', () => {
  it('opens Runs for anything it does not know', () => {
    expect(workflowsTabFromParam('profiles')).toBe('profiles')
    expect(workflowsTabFromParam(['templates'])).toBe('templates')
    expect(workflowsTabFromParam('nonsense')).toBe('runs')
    expect(workflowsTabFromParam(undefined)).toBe('runs')
  })
})

describe('WorkflowsDialog', () => {
  it('reads the library from the first online computer', async () => {
    renderDialog()
    expect(await screen.findByText('Ship it')).toBeInTheDocument()
    expect(rpc.listWorkflowLibrary).toHaveBeenCalledWith('mac', {})
    expect(screen.getByTestId('workflow-library-computer')).toHaveTextContent('Mac Studio')
  })

  it('adds the example workflow to the library computer and reloads it', async () => {
    renderDialog()
    await screen.findByText('Ship it')
    fireEvent.click(screen.getByRole('button', { name: 'Add example' }))
    await waitFor(() => expect(rpc.installExampleWorkflow).toHaveBeenCalledWith('mac', {}))
    await waitFor(() => expect(rpc.listWorkflowLibrary).toHaveBeenCalledTimes(2))
  })

  it('shows the engine\'s validation error inline when a template does not save', async () => {
    rpc.saveWorkflowTemplate.mockRejectedValueOnce(new Error('invalid workflow definition: template "Ship it" needs exactly one Lead role, has 0'))
    renderDialog()
    await screen.findByText('Ship it')
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Save template' }))
    expect(await screen.findByText('Template "Ship it" needs exactly one Lead role, has 0')).toBeInTheDocument()
    expect(rpc.saveWorkflowTemplate).toHaveBeenCalledWith('mac', { template: expect.objectContaining({ id: 't-1', name: 'Ship it' }) })
    // The editor stays open so the user can correct it.
    expect(screen.getByTestId('workflow-template-editor')).toBeInTheDocument()
  })

  it('lists the profiles on the Profiles tab', async () => {
    const { setTab } = renderDialog('runs')
    setTab('profiles')
    expect(await screen.findByText('Builder')).toBeInTheDocument()
  })
})
