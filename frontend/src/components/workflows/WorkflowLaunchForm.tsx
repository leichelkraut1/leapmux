import type { LaunchedWorkflow } from './launchWorkflowRun'
import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentProfile, WorkflowTemplate } from '~/generated/proto/leapmux/v1/workflow_pb'
import type { RoleLaunchDraft, WorkflowComputer } from '~/stores/workflows'
import { createEffect, createMemo, createSignal, For, on, Show } from 'solid-js'
import { createStore } from 'solid-js/store'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Alert } from '~/components/common/Alert'
import { WorkerPlacementStrategy } from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  buildLaunchOverrides,
  computerName,
  decideCoordinator,
  defaultLeadComputer,
  leadRoleOf,
  missingProfileRoles,
  modelSummary,
  offlinePinnedComputer,
  placementFit,
  placementSummary,
  referencedProfiles,
  resolveRoleAgent,
} from '~/stores/workflows'
import { warningText } from '~/styles/shared.css'
import { ComputerLabel, ComputerPicker, StatusBadge } from './ComputerPicker'
import { createWorkflowAction } from './createWorkflowAction'
import { launchWorkflowRun } from './launchWorkflowRun'
import * as styles from './Workflows.css'

export interface WorkflowLaunchFormProps {
  template: WorkflowTemplate
  profiles: readonly AgentProfile[]
  computers: readonly WorkflowComputer[]
  /** The workspace the agents' tabs go into: the active one. */
  workspaceId: string | undefined
  /** Why no tab can be placed right now; launching is refused while set. */
  blockedReason?: string
  homeDirOf: (workerId: string) => string
  placeLeadTab: (agent: AgentInfo) => boolean
  onLaunched: (launched: LaunchedWorkflow) => void
  onCancel: () => void
}

export function WorkflowLaunchForm(props: WorkflowLaunchFormProps) {
  const lead = createMemo(() => leadRoleOf(props.template.roles))
  const resolved = (roleKey: string) => {
    const role = props.template.roles.find(r => r.key === roleKey)
    return role ? resolveRoleAgent(role, props.profiles) : undefined
  }
  const leadPlacement = () => {
    const role = lead()
    return role ? resolved(role.key)?.placement : undefined
  }

  // eslint-disable-next-line solid/reactivity -- the template's name seeds the field once
  const [name, setName] = createSignal(props.template.name)
  const [objective, setObjective] = createSignal('')
  const [projectDir, setProjectDir] = createSignal('')
  const [projectDirTouched, setProjectDirTouched] = createSignal(false)
  const [baseBranch, setBaseBranch] = createSignal('')
  const [chosenLead, setChosenLead] = createSignal('')
  const [drafts, setDrafts] = createStore<RoleLaunchDraft[]>(
    // eslint-disable-next-line solid/reactivity -- one draft per role, seeded once
    props.template.roles.map(role => ({ roleKey: role.key, workerId: '', model: '', effort: '' })),
  )
  const launch = createWorkflowAction()

  const coordinator = createMemo(() => decideCoordinator(leadPlacement(), chosenLead()))
  // Default the Lead's computer once the computers are known; never replace a pick.
  createEffect(() => {
    if (!coordinator().pinned && !chosenLead()) {
      const pick = defaultLeadComputer(leadPlacement(), props.computers)
      if (pick)
        setChosenLead(pick)
    }
  })
  // The project directory follows the coordinator's home until the user types one.
  // Tracked on the home directory too, which arrives with the computer's info.
  createEffect(on(() => (coordinator().workerId ? props.homeDirOf(coordinator().workerId) : ''), (home) => {
    if (!projectDirTouched() && home)
      setProjectDir(home)
  }))

  const missing = createMemo(() => missingProfileRoles(props.template, props.profiles))
  const coordinatorComputer = () => props.computers.find(c => c.id === coordinator().workerId)
  const leadFit = () => {
    const c = coordinatorComputer()
    return c ? placementFit(leadPlacement(), c) : 'unknown'
  }
  const refusal = (): string | undefined => {
    if (props.blockedReason)
      return props.blockedReason
    if (!props.workspaceId)
      return 'Open a workspace first: the agents\' tabs go into the active workspace.'
    if (!lead())
      return 'This template has no Lead role.'
    if (missing().length > 0)
      return `The library has no profile for role${missing().length === 1 ? '' : 's'} ${missing().join(', ')}.`
    if (!coordinator().workerId)
      return 'Choose the computer the Lead runs on.'
    if (!objective().trim())
      return 'Describe the objective.'
    if (!projectDir().trim())
      return 'Enter the project directory.'
    return undefined
  }

  const submit = (e: Event) => {
    e.preventDefault()
    if (refusal())
      return
    void launch.run(async () => {
      const leadKey = lead()?.key ?? ''
      const launched = await launchWorkflowRun({
        coordinatorWorkerId: coordinator().workerId,
        template: props.template,
        profiles: referencedProfiles(props.template, props.profiles),
        overrides: buildLaunchOverrides(drafts, leadKey),
        name: name(),
        objective: objective(),
        projectDir: projectDir(),
        baseBranch: baseBranch(),
        workspaceId: props.workspaceId ?? '',
        placeLeadTab: props.placeLeadTab,
      })
      props.onLaunched(launched)
    })
  }

  const nameOf = (id: string) => computerName(props.computers, id)

  return (
    <form class={styles.form} onSubmit={submit} aria-label="Launch workflow" data-testid="workflow-launch-form">
      <h3 class={styles.sectionTitle}>{`Launch ${props.template.name}`}</h3>
      <div class={styles.grid2}>
        <label class={styles.field}>
          Run name
          <input type="text" value={name()} onInput={e => setName(e.currentTarget.value)} />
        </label>
        <label class={styles.field}>
          Base branch
          <input type="text" placeholder="The project's current branch" value={baseBranch()} onInput={e => setBaseBranch(e.currentTarget.value)} />
        </label>
      </div>
      <label class={styles.field}>
        Objective
        <textarea
          class={styles.textarea}
          required
          placeholder="What should the team deliver?"
          value={objective()}
          onInput={e => setObjective(e.currentTarget.value)}
          data-testid="workflow-launch-objective"
        />
      </label>

      <fieldset class={styles.fieldset}>
        <legend>Lead and coordinator</legend>
        <p class={styles.hint}>The run is coordinated on the Lead's computer and its state is stored there.</p>
        <Show
          when={!coordinator().pinned}
          fallback={(
            <div class={styles.field}>
              <span>The Lead is pinned to</span>
              <ComputerLabel computers={props.computers} workerId={coordinator().workerId} />
            </div>
          )}
        >
          <div class={styles.field}>
            <span>Lead computer</span>
            <ComputerPicker
              label="Lead computer"
              computers={props.computers}
              value={chosenLead()}
              onChange={setChosenLead}
              testId="workflow-launch-lead-computer"
            />
            <Show when={leadFit() === 'excluded'}>
              <span class={warningText} role="status">The template's Lead placement does not allow this computer; the coordinator will refuse it.</span>
            </Show>
          </div>
        </Show>
        <Show when={coordinatorComputer() && !coordinatorComputer()?.online}>
          <span class={warningText} role="status" data-testid="workflow-launch-lead-offline">
            {`${nameOf(coordinator().workerId)} is offline. The run can only be created once it is back.`}
          </span>
        </Show>
        <label class={styles.field}>
          Project directory
          <input
            type="text"
            value={projectDir()}
            onInput={(e) => {
              setProjectDirTouched(true)
              setProjectDir(e.currentTarget.value)
            }}
            data-testid="workflow-launch-project-dir"
          />
          <span class={styles.hint}>On the Lead's computer. Roles without their own directory work here.</span>
        </label>
      </fieldset>

      <fieldset class={styles.fieldset}>
        <legend>Roles</legend>
        <ul class={styles.list} aria-label="Roles">
          <For each={props.template.roles}>
            {(role, index) => {
              const agent = () => resolved(role.key)
              const draft = () => drafts[index()]
              // What the role resolves to WITH the user's pick, so the warning
              // speaks about the computer that will actually be used.
              const effectivePlacement = () => {
                const picked = draft()?.workerId
                return picked
                  ? { strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: picked, requiredCapabilities: [], allowedWorkerIds: [] }
                  : agent()?.placement
              }
              const offline = () => (role.isLead ? undefined : offlinePinnedComputer(effectivePlacement(), props.computers))
              return (
                <li class={styles.card} data-testid="workflow-launch-role" data-role-key={role.key}>
                  <div class={styles.inline}>
                    <strong>{role.name || role.key}</strong>
                    <span class={styles.secondary}>{role.key}</span>
                    <Show when={role.isLead}>
                      <StatusBadge label="Lead" tone="neutral" />
                    </Show>
                  </div>
                  <span class={styles.secondary}>
                    {`${agentProviderLabel(agent()?.provider)} · ${modelSummary(agent()?.options)} · ${role.isLead ? `On ${nameOf(coordinator().workerId)}` : placementSummary(agent()?.placement, nameOf)}`}
                  </span>
                  <div class={styles.grid3}>
                    <Show when={!role.isLead}>
                      <div class={styles.field}>
                        <span>Computer</span>
                        <ComputerPicker
                          label={`Computer for ${role.name || role.key}`}
                          computers={props.computers}
                          value={draft()?.workerId ?? ''}
                          emptyLabel="Keep the template's placement"
                          onChange={workerId => setDrafts(index(), 'workerId', workerId)}
                          testId={`workflow-launch-role-computer-${role.key}`}
                        />
                      </div>
                    </Show>
                    <label class={styles.field}>
                      Model
                      <input type="text" placeholder="Keep" value={draft()?.model ?? ''} onInput={e => setDrafts(index(), 'model', e.currentTarget.value)} />
                    </label>
                    <label class={styles.field}>
                      Effort
                      <input type="text" placeholder="Keep" value={draft()?.effort ?? ''} onInput={e => setDrafts(index(), 'effort', e.currentTarget.value)} />
                    </label>
                  </div>
                  <Show when={offline()}>
                    {workerId => (
                      <span class={warningText} role="status" data-testid="workflow-launch-offline-warning">
                        {`${nameOf(workerId())} is offline. This role waits for it; no other computer is used.`}
                      </span>
                    )}
                  </Show>
                </li>
              )
            }}
          </For>
        </ul>
      </fieldset>

      <Show when={refusal()}>
        {reason => <p class={styles.hint} data-testid="workflow-launch-refusal">{reason()}</p>}
      </Show>
      <Show when={launch.error()}>
        {message => <Alert variant="danger">{message()}</Alert>}
      </Show>
      <div class={styles.footer}>
        <button type="button" class="outline" onClick={() => props.onCancel()} disabled={launch.busy()}>Cancel</button>
        <button type="submit" disabled={launch.busy() || refusal() !== undefined}>{launch.busy() ? 'Launching...' : 'Launch'}</button>
      </div>
    </form>
  )
}
