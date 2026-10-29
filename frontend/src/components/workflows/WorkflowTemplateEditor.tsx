import type { AgentProfile, WorkflowTemplate } from '~/generated/proto/leapmux/v1/workflow_pb'
import type { RoleDraft, StepDraft, TemplateDraft, WorkflowComputer } from '~/stores/workflows'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, For, Index, Show } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { PillGroup } from '~/components/common/PillGroup'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { WorkflowApprovalGate, WorkflowGitIsolation } from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  removeStep,
  renameStepKey,
  REQUIRED_OUTPUTS,
  roleDraft,
  setLeadRole,
  stepDraft,
  templateInit,
} from '~/stores/workflows'
import { fieldTriggerChevron } from '~/styles/shared.css'
import { createWorkflowAction } from './createWorkflowAction'
import { OptionFieldsEditor } from './OptionFieldsEditor'
import { PlacementEditor } from './PlacementEditor'
import { ProviderPicker } from './ProviderPicker'
import * as styles from './Workflows.css'

const ISOLATION = [
  { key: WorkflowGitIsolation.SHARED, label: 'Shared' },
  { key: WorkflowGitIsolation.WORKTREE, label: 'Worktree' },
] as const

const GATES = [
  { key: WorkflowApprovalGate.NONE, label: 'None' },
  { key: WorkflowApprovalGate.BEFORE_START, label: 'Before start' },
  { key: WorkflowApprovalGate.BEFORE_COMPLETE, label: 'Before complete' },
] as const

/** A field-trigger dropdown with one radio row per option. */
function ChoiceMenu<K extends string>(props: {
  label: string
  value: K
  options: readonly { key: K, label: string }[]
  onChange: (key: K) => void
  placeholder: string
  testId?: string
}) {
  const current = () => props.options.find(o => o.key === props.value)?.label
  return (
    <DropdownMenu
      class={styles.menu}
      aria-label={props.label}
      matchTriggerWidth
      trigger={trigger => (
        <button
          type="button"
          class={styles.trigger}
          aria-label={props.label}
          aria-expanded={trigger['aria-expanded']}
          ref={trigger.ref}
          onPointerDown={trigger.onPointerDown}
          onClick={trigger.onClick}
          data-testid={props.testId}
        >
          <span class={styles.triggerValue}>{current() ?? props.placeholder}</span>
          <ChevronDown size={16} class={fieldTriggerChevron} />
        </button>
      )}
    >
      <For each={props.options}>
        {option => (
          <DropdownMenuCheckableItem kind="radio" label={option.label} checked={option.key === props.value} onSelect={() => props.onChange(option.key)} />
        )}
      </For>
    </DropdownMenu>
  )
}

/** Several step keys at once; the menu stays open while ticking. */
function DependsOnPicker(props: { label: string, options: readonly string[], value: readonly string[], onChange: (next: string[]) => void }) {
  const toggle = (key: string) => props.onChange(props.value.includes(key) ? props.value.filter(k => k !== key) : [...props.value, key])
  return (
    <DropdownMenu
      as="div"
      class={styles.menu}
      aria-label={props.label}
      matchTriggerWidth
      trigger={trigger => (
        <button
          type="button"
          class={styles.trigger}
          aria-label={props.label}
          aria-expanded={trigger['aria-expanded']}
          ref={trigger.ref}
          onPointerDown={trigger.onPointerDown}
          onClick={trigger.onClick}
        >
          <span class={styles.triggerValue}>{props.value.length > 0 ? props.value.join(', ') : 'No dependencies'}</span>
          <ChevronDown size={16} class={fieldTriggerChevron} />
        </button>
      )}
    >
      <Show when={props.options.length > 0} fallback={<p class={styles.hint}>Add another step with a key first.</p>}>
        <For each={props.options}>
          {key => <DropdownMenuCheckableItem kind="checkbox" label={key} checked={props.value.includes(key)} onSelect={() => toggle(key)} />}
        </For>
      </Show>
    </DropdownMenu>
  )
}

export interface WorkflowTemplateEditorProps {
  initial: TemplateDraft
  libraryWorkerId: string
  profiles: readonly AgentProfile[]
  computers: readonly WorkflowComputer[]
  availableProviders?: AgentProvider[]
  onSaved: (template: WorkflowTemplate) => void
  onCancel: () => void
}

export function WorkflowTemplateEditor(props: WorkflowTemplateEditorProps) {
  // The draft is copied once; the editor owns it from here.
  const [draft, setDraft] = createStore<TemplateDraft>(structuredClone(props.initial))
  const save = createWorkflowAction()

  const profileOptions = createMemo(() => [
    { key: '', label: 'No profile (provider below)' },
    ...props.profiles.map(p => ({ key: p.id, label: p.name })),
  ])
  const workerRoles = createMemo(() => draft.roles
    .filter(r => !r.isLead && r.key.trim())
    .map(r => ({ key: r.key, label: r.name ? `${r.name} (${r.key})` : r.key })))

  const setRole = (index: number, patch: Partial<RoleDraft>) => setDraft('roles', index, patch)
  const renameRole = (index: number, key: string) => {
    const previous = draft.roles[index]?.key ?? ''
    setDraft(produce((d) => {
      const role = d.roles[index]
      if (role)
        role.key = key
      if (previous) {
        for (const step of d.steps) {
          if (step.roleKey === previous)
            step.roleKey = key
        }
      }
    }))
  }
  const setStep = (index: number, patch: Partial<StepDraft>) => setDraft('steps', index, patch)
  const profileProvider = (role: RoleDraft) => props.profiles.find(p => p.id === role.profileId)?.agentProvider ?? AgentProvider.UNSPECIFIED
  const effectiveProvider = (role: RoleDraft) => {
    const provider = role.provider !== AgentProvider.UNSPECIFIED ? role.provider : profileProvider(role)
    return provider === AgentProvider.UNSPECIFIED ? undefined : provider
  }

  const submit = (e: Event) => {
    e.preventDefault()
    void save.run(async () => {
      const resp = await workerRpc.saveWorkflowTemplate(props.libraryWorkerId, { template: templateInit(draft) })
      if (resp.template)
        props.onSaved(resp.template)
    })
  }

  return (
    <form class={styles.form} onSubmit={submit} aria-label="Workflow template" data-testid="workflow-template-editor">
      <h3 class={styles.sectionTitle}>{draft.id ? `Edit ${props.initial.name || 'template'}` : 'New template'}</h3>
      <div class={styles.grid2}>
        <label class={styles.field}>
          Name
          <input type="text" required value={draft.name} onInput={e => setDraft('name', e.currentTarget.value)} />
        </label>
        <label class={styles.field}>
          Description
          <input type="text" value={draft.description} onInput={e => setDraft('description', e.currentTarget.value)} />
        </label>
      </div>
      <label class={styles.field}>
        Process instructions for the Lead (Markdown)
        <textarea class={styles.textarea} value={draft.instructions} onInput={e => setDraft('instructions', e.currentTarget.value)} />
      </label>
      <label class={styles.field}>
        Completion criteria
        <textarea class={styles.textarea} value={draft.completionCriteria} onInput={e => setDraft('completionCriteria', e.currentTarget.value)} />
      </label>

      <fieldset class={styles.fieldset}>
        <legend>Roles</legend>
        <p class={styles.hint}>Exactly one role is the Lead. The Lead coordinates; the other roles take the steps.</p>
        <Index each={draft.roles}>
          {(role, index) => (
            <div class={styles.card} data-testid="workflow-role-editor">
              <div class={styles.grid3}>
                <label class={styles.field}>
                  Key
                  <input type="text" placeholder="builder" value={role().key} onInput={e => renameRole(index, e.currentTarget.value)} />
                </label>
                <label class={styles.field}>
                  Name
                  <input type="text" value={role().name} onInput={e => setRole(index, { name: e.currentTarget.value })} />
                </label>
                <div class={styles.field}>
                  <span>Profile</span>
                  <ChoiceMenu
                    label={`Profile for role ${role().key || index + 1}`}
                    value={role().profileId}
                    options={profileOptions()}
                    onChange={profileId => setRole(index, { profileId })}
                    placeholder="Unknown profile"
                  />
                </div>
              </div>
              <div class={styles.inline}>
                <label class={styles.checkboxLabel}>
                  <input
                    type="radio"
                    name="workflow-lead-role"
                    checked={role().isLead}
                    onChange={() => setDraft('roles', setLeadRole(draft.roles, index))}
                  />
                  Lead
                </label>
                <label class={styles.checkboxLabel}>
                  <input type="checkbox" checked={role().reviewRequired} onChange={e => setRole(index, { reviewRequired: e.currentTarget.checked })} />
                  Lead reviews every result
                </label>
              </div>
              <div class={styles.grid2}>
                <div class={styles.field}>
                  <span>Git isolation</span>
                  <PillGroup<WorkflowGitIsolation>
                    label="Git isolation"
                    options={ISOLATION}
                    selectedKey={role().gitIsolation}
                    onSelect={gitIsolation => setRole(index, { gitIsolation })}
                    small
                  />
                </div>
                <div class={styles.field}>
                  <span>Required outputs</span>
                  <div class={styles.inline}>
                    <For each={REQUIRED_OUTPUTS}>
                      {output => (
                        <label class={styles.checkboxLabel}>
                          <input
                            type="checkbox"
                            checked={role().requiredOutputs.includes(output.key)}
                            onChange={(e) => {
                              const others = role().requiredOutputs.filter(o => o !== output.key)
                              setRole(index, { requiredOutputs: e.currentTarget.checked ? [...others, output.key] : others })
                            }}
                          />
                          {output.label}
                        </label>
                      )}
                    </For>
                  </div>
                </div>
              </div>
              <details>
                <summary>Overrides</summary>
                <div class={styles.form}>
                  <div class={styles.field}>
                    <span>Provider</span>
                    <ProviderPicker
                      label={`Provider for role ${role().key || index + 1}`}
                      value={role().provider}
                      inheritLabel="From the profile"
                      onChange={(provider) => {
                        if (provider !== role().provider)
                          setRole(index, { provider, options: { ...role().options, rest: {} } })
                      }}
                      {...(props.availableProviders !== undefined ? { availableProviders: props.availableProviders } : {})}
                    />
                  </div>
                  <OptionFieldsEditor
                    provider={effectiveProvider(role())}
                    value={role().options}
                    onChange={options => setRole(index, { options })}
                    idPrefix={`role-${index}`}
                  />
                  <label class={styles.checkboxLabel}>
                    <input type="checkbox" checked={role().overridePlacement} onChange={e => setRole(index, { overridePlacement: e.currentTarget.checked })} />
                    Override the profile's placement
                  </label>
                  <Show when={role().overridePlacement}>
                    <PlacementEditor
                      value={role().placement}
                      onChange={placement => setRole(index, { placement })}
                      computers={props.computers}
                      idPrefix={`role-${index}`}
                    />
                  </Show>
                </div>
              </details>
              <div class={styles.actions}>
                <button
                  type="button"
                  class="outline small"
                  data-variant="danger"
                  onClick={() => setDraft('roles', roles => roles.filter((_, i) => i !== index))}
                >
                  Remove role
                </button>
              </div>
            </div>
          )}
        </Index>
        <div>
          <button
            type="button"
            class="outline small"
            onClick={() => setDraft('roles', roles => [...roles, { ...roleDraft(), isLead: roles.length === 0 }])}
          >
            Add role
          </button>
        </div>
      </fieldset>

      <fieldset class={styles.fieldset}>
        <legend>Steps</legend>
        <p class={styles.hint}>The default process. Each step becomes a task the Lead releases (or that starts at launch).</p>
        <Index each={draft.steps}>
          {(step, index) => (
            <div class={styles.card} data-testid="workflow-step-editor">
              <div class={styles.grid3}>
                <label class={styles.field}>
                  Key
                  <input type="text" placeholder="build" value={step().key} onInput={e => setDraft('steps', renameStepKey(draft.steps, index, e.currentTarget.value))} />
                </label>
                <label class={styles.field}>
                  Title
                  <input type="text" value={step().title} onInput={e => setStep(index, { title: e.currentTarget.value })} />
                </label>
                <div class={styles.field}>
                  <span>Role</span>
                  <ChoiceMenu
                    label={`Role for step ${step().key || index + 1}`}
                    value={step().roleKey}
                    options={workerRoles()}
                    onChange={roleKey => setStep(index, { roleKey })}
                    placeholder="Choose a role"
                  />
                </div>
              </div>
              <div class={styles.grid2}>
                <div class={styles.field}>
                  <span>Depends on</span>
                  <DependsOnPicker
                    label={`Dependencies of step ${step().key || index + 1}`}
                    options={draft.steps.filter((s, i) => i !== index && s.key.trim()).map(s => s.key)}
                    value={step().dependsOn}
                    onChange={dependsOn => setStep(index, { dependsOn })}
                  />
                </div>
                <div class={styles.field}>
                  <span>Approval gate</span>
                  <PillGroup<WorkflowApprovalGate>
                    label="Approval gate"
                    options={GATES}
                    selectedKey={step().approval}
                    onSelect={approval => setStep(index, { approval })}
                    small
                  />
                </div>
              </div>
              <div class={styles.inline}>
                <label class={styles.checkboxLabel}>
                  <input type="checkbox" checked={step().autoStart} onChange={e => setStep(index, { autoStart: e.currentTarget.checked })} />
                  Start at launch
                </label>
                <label class={styles.checkboxLabel}>
                  Max attempts
                  <input type="number" min={0} value={step().maxAttempts} onInput={e => setStep(index, { maxAttempts: e.currentTarget.valueAsNumber || 0 })} />
                </label>
                <span class={styles.hint}>0 uses the template default.</span>
              </div>
              <label class={styles.field}>
                Instructions (Markdown)
                <textarea class={styles.textarea} value={step().instructions} onInput={e => setStep(index, { instructions: e.currentTarget.value })} />
              </label>
              <div class={styles.actions}>
                <button type="button" class="outline small" data-variant="danger" onClick={() => setDraft('steps', removeStep(draft.steps, index))}>
                  Remove step
                </button>
              </div>
            </div>
          )}
        </Index>
        <div>
          <button type="button" class="outline small" onClick={() => setDraft('steps', steps => [...steps, stepDraft()])}>Add step</button>
        </div>
      </fieldset>

      <fieldset class={styles.fieldset}>
        <legend>Settings</legend>
        <div class={styles.grid3}>
          <label class={styles.field}>
            Default max attempts
            <input type="number" min={0} value={draft.defaultMaxAttempts} onInput={e => setDraft('defaultMaxAttempts', e.currentTarget.valueAsNumber || 0)} />
            <span class={styles.hint}>0 means 3.</span>
          </label>
          <label class={styles.field}>
            Max parallel tasks
            <input type="number" min={0} value={draft.maxParallelTasks} onInput={e => setDraft('maxParallelTasks', e.currentTarget.valueAsNumber || 0)} />
            <span class={styles.hint}>0 means 1 (one at a time).</span>
          </label>
          <label class={styles.field}>
            Max agents
            <input type="number" min={0} value={draft.maxAgents} onInput={e => setDraft('maxAgents', e.currentTarget.valueAsNumber || 0)} />
            <span class={styles.hint}>Lead included. 0 means 8.</span>
          </label>
        </div>
        <label class={styles.checkboxLabel}>
          <input type="checkbox" checked={draft.requireFinalApproval} onChange={e => setDraft('requireFinalApproval', e.currentTarget.checked)} />
          Require my approval before the run completes
        </label>
        <label class={styles.checkboxLabel}>
          <input type="checkbox" checked={draft.confineAgents} onChange={e => setDraft('confineAgents', e.currentTarget.checked)} />
          Restrict agents to this workflow (off: they keep full control and can open helper agents)
        </label>
        <label class={styles.field}>
          Sensitive operations (one per line)
          <textarea
            class={styles.textarea}
            placeholder={'git push\nmerge to main'}
            value={draft.sensitiveOperations}
            onInput={e => setDraft('sensitiveOperations', e.currentTarget.value)}
          />
          <span class={styles.hint}>The Lead asks you before any of these.</span>
        </label>
      </fieldset>

      <Show when={save.error()}>
        {message => <Alert variant="danger">{message()}</Alert>}
      </Show>
      <div class={styles.footer}>
        <button type="button" class="outline" onClick={() => props.onCancel()} disabled={save.busy()}>Cancel</button>
        <button type="submit" disabled={save.busy()}>{save.busy() ? 'Saving...' : 'Save template'}</button>
      </div>
    </form>
  )
}
