import type { LaunchedWorkflow } from './launchWorkflowRun'
import type { AgentInfo, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentProfile, WorkflowTemplate } from '~/generated/proto/leapmux/v1/workflow_pb'
import type { RunRow, TemplateDraft, WorkflowComputer } from '~/stores/workflows'
import { createEffect, createSignal, on, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { Dialog } from '~/components/common/Dialog'
import { PillGroup } from '~/components/common/PillGroup'
import { RefreshButton } from '~/components/common/RefreshButton'
import { defaultComputerId, newTemplateDraft, templateDraft, workflowErrorMessage } from '~/stores/workflows'
import { ComputerPicker } from './ComputerPicker'
import { WorkflowLaunchForm } from './WorkflowLaunchForm'
import { WorkflowProfiles } from './WorkflowProfiles'
import { WorkflowRunsList } from './WorkflowRunsList'
import * as styles from './Workflows.css'
import { WorkflowTemplateEditor } from './WorkflowTemplateEditor'
import { WorkflowTemplates } from './WorkflowTemplates'

export type WorkflowsTab = 'runs' | 'templates' | 'profiles'

const TABS = [
  { key: 'runs', label: 'Runs' },
  { key: 'templates', label: 'Templates' },
  { key: 'profiles', label: 'Profiles' },
] as const

/** The `?workflows=` value as a tab; anything else opens Runs. */
export function workflowsTabFromParam(value: string | string[] | undefined): WorkflowsTab {
  const raw = Array.isArray(value) ? value[0] : value
  return raw === 'templates' || raw === 'profiles' ? raw : 'runs'
}

type TemplatesView
  = | { kind: 'list' }
    | { kind: 'edit', draft: TemplateDraft }
    | { kind: 'launch', template: WorkflowTemplate }

export interface WorkflowsDialogProps {
  tab: WorkflowsTab
  onTabChange: (tab: WorkflowsTab) => void
  computers: readonly WorkflowComputer[]
  workspaceId: string | undefined
  launchBlockedReason?: string
  availableProviders?: AgentProvider[]
  homeDirOf: (workerId: string) => string
  placeLeadTab: (agent: AgentInfo) => boolean
  onLaunched: (launched: LaunchedWorkflow) => void
  onOpenRun: (row: RunRow) => void
  onClose: () => void
}

export function WorkflowsDialog(props: WorkflowsDialogProps) {
  // The library computer: where profiles and templates are read and saved.
  // Not persisted -- the first online computer is the default every time.
  const [libraryWorkerId, setLibraryWorkerId] = createSignal('')
  createEffect(() => {
    const current = libraryWorkerId()
    const ids = props.computers.map(c => c.id)
    if (!current || !ids.includes(current)) {
      const pick = defaultComputerId(props.computers)
      if (pick !== current)
        setLibraryWorkerId(pick)
    }
  })

  const [profiles, setProfiles] = createSignal<AgentProfile[]>([])
  const [templates, setTemplates] = createSignal<WorkflowTemplate[]>([])
  const [libraryError, setLibraryError] = createSignal<string | null>(null)
  const [libraryLoading, setLibraryLoading] = createSignal(false)
  const [templatesView, setTemplatesView] = createSignal<TemplatesView>({ kind: 'list' })
  let generation = 0

  const loadLibrary = async () => {
    const workerId = libraryWorkerId()
    const mine = ++generation
    if (!workerId) {
      setProfiles([])
      setTemplates([])
      return
    }
    setLibraryLoading(true)
    try {
      const resp = await workerRpc.listWorkflowLibrary(workerId, {})
      if (mine !== generation)
        return
      setProfiles(resp.profiles)
      setTemplates(resp.templates)
      setLibraryError(null)
    }
    catch (err) {
      if (mine === generation)
        setLibraryError(workflowErrorMessage(err, 'Could not read the library on that computer.'))
    }
    finally {
      if (mine === generation)
        setLibraryLoading(false)
    }
  }

  createEffect(on(libraryWorkerId, () => {
    setTemplatesView({ kind: 'list' })
    void loadLibrary()
  }))

  const libraryComputer = () => props.computers.find(c => c.id === libraryWorkerId())

  return (
    <Dialog title="Workflows" wide tall onClose={props.onClose} data-testid="workflows-dialog">
      <div class={styles.dialogBody}>
        <div class={styles.toolbar}>
          <PillGroup<WorkflowsTab>
            label="Workflows section"
            options={TABS}
            selectedKey={props.tab}
            onSelect={props.onTabChange}
          />
          <Show when={props.tab !== 'runs'}>
            <div class={styles.toolbarField}>
              <span>Library computer</span>
              <div class={styles.inline}>
                <div class={styles.grow}>
                  <ComputerPicker
                    label="Library computer"
                    computers={props.computers}
                    value={libraryWorkerId()}
                    onChange={setLibraryWorkerId}
                    testId="workflow-library-computer"
                  />
                </div>
                <RefreshButton onClick={() => void loadLibrary()} disabled={libraryLoading()} title="Reload the library" />
              </div>
            </div>
          </Show>
        </div>

        <div class={styles.scroll}>
          <Show when={props.tab === 'runs'}>
            <WorkflowRunsList computers={props.computers} onOpenRun={props.onOpenRun} />
          </Show>

          <Show when={props.tab !== 'runs'}>
            <Show when={libraryComputer() && !libraryComputer()?.online}>
              <Alert variant="warning">This computer is offline, so its library cannot be read or changed.</Alert>
            </Show>
            <Show when={libraryError()}>
              {message => <Alert variant="danger">{message()}</Alert>}
            </Show>
            <Show when={props.computers.length === 0}>
              <div class={styles.empty}>
                <strong>No computers</strong>
                <p>Register a computer to keep a workflow library on it.</p>
              </div>
            </Show>
          </Show>

          <Show when={props.tab === 'profiles' && libraryWorkerId()}>
            <WorkflowProfiles
              libraryWorkerId={libraryWorkerId()}
              profiles={profiles()}
              computers={props.computers}
              {...(props.availableProviders !== undefined ? { availableProviders: props.availableProviders } : {})}
              onChanged={() => void loadLibrary()}
            />
          </Show>

          <Show when={props.tab === 'templates' && libraryWorkerId()}>
            <Show when={templatesView()} keyed>
              {(view) => {
                switch (view.kind) {
                  case 'edit':
                    return (
                      <WorkflowTemplateEditor
                        initial={view.draft}
                        libraryWorkerId={libraryWorkerId()}
                        profiles={profiles()}
                        computers={props.computers}
                        {...(props.availableProviders !== undefined ? { availableProviders: props.availableProviders } : {})}
                        onSaved={() => {
                          setTemplatesView({ kind: 'list' })
                          void loadLibrary()
                        }}
                        onCancel={() => setTemplatesView({ kind: 'list' })}
                      />
                    )
                  case 'launch':
                    return (
                      <WorkflowLaunchForm
                        template={view.template}
                        profiles={profiles()}
                        computers={props.computers}
                        workspaceId={props.workspaceId}
                        {...(props.launchBlockedReason !== undefined ? { blockedReason: props.launchBlockedReason } : {})}
                        homeDirOf={props.homeDirOf}
                        placeLeadTab={props.placeLeadTab}
                        onLaunched={props.onLaunched}
                        onCancel={() => setTemplatesView({ kind: 'list' })}
                      />
                    )
                  default:
                    return (
                      <WorkflowTemplates
                        libraryWorkerId={libraryWorkerId()}
                        templates={templates()}
                        profiles={profiles()}
                        onChanged={() => void loadLibrary()}
                        onNew={() => setTemplatesView({ kind: 'edit', draft: newTemplateDraft() })}
                        onEdit={template => setTemplatesView({ kind: 'edit', draft: templateDraft(template) })}
                        onLaunch={template => setTemplatesView({ kind: 'launch', template })}
                      />
                    )
                }
              }}
            </Show>
          </Show>
        </div>
      </div>
    </Dialog>
  )
}
