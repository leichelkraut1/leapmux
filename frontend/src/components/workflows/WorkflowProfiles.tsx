import type { AgentProfile } from '~/generated/proto/leapmux/v1/workflow_pb'
import type { ProfileDraft, WorkflowComputer } from '~/stores/workflows'
import { createSignal, For, Show } from 'solid-js'
import { createStore } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { Alert } from '~/components/common/Alert'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { computerName, modelSummary, placementSummary, profileDraft, profileInit } from '~/stores/workflows'
import { createWorkflowAction } from './createWorkflowAction'
import { OptionFieldsEditor } from './OptionFieldsEditor'
import { PlacementEditor } from './PlacementEditor'
import * as styles from './Workflows.css'

export interface WorkflowProfilesProps {
  libraryWorkerId: string
  profiles: readonly AgentProfile[]
  computers: readonly WorkflowComputer[]
  availableProviders?: AgentProvider[]
  onChanged: () => void
}

export function WorkflowProfiles(props: WorkflowProfilesProps) {
  const [editing, setEditing] = createSignal<ProfileDraft>()
  const remove = createWorkflowAction()
  const nameOf = (id: string) => computerName(props.computers, id)

  return (
    <Show
      when={editing()}
      keyed
      fallback={(
        <div class={styles.form}>
          <div class={styles.sectionHeader}>
            <p class={styles.description}>A profile is a reusable agent: provider, model, where it runs and its standing instructions.</p>
            <button type="button" onClick={() => setEditing(profileDraft())} disabled={!props.libraryWorkerId}>New profile</button>
          </div>
          <Show when={remove.error()}>
            {message => <Alert variant="danger">{message()}</Alert>}
          </Show>
          <Show
            when={props.profiles.length > 0}
            fallback={(
              <div class={styles.empty}>
                <strong>No profiles yet</strong>
                <p>Create one, or add the example workflow from the Templates tab.</p>
              </div>
            )}
          >
            <ul class={styles.list} aria-label="Agent profiles">
              <For each={props.profiles}>
                {profile => (
                  <li class={styles.row} data-testid="workflow-profile-row">
                    <div class={styles.identity}>
                      <h3 class={styles.title}>{profile.name}</h3>
                      <Show when={profile.description}>
                        <span class={styles.secondary}>{profile.description}</span>
                      </Show>
                      <span class={styles.secondary}>
                        {`${agentProviderLabel(profile.agentProvider)} · ${modelSummary(profile.options)} · ${placementSummary(profile.placement, nameOf)}`}
                      </span>
                    </div>
                    <div class={styles.actions}>
                      <button type="button" class="outline small" onClick={() => setEditing(profileDraft(profile))}>Edit</button>
                      <ConfirmButton
                        class="outline small"
                        data-variant="danger"
                        confirmLabel="Delete?"
                        disabled={remove.busy()}
                        onClick={() => void remove.run(async () => {
                          await workerRpc.deleteAgentProfile(props.libraryWorkerId, { profileId: profile.id })
                          props.onChanged()
                        })}
                      >
                        Delete
                      </ConfirmButton>
                    </div>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </div>
      )}
    >
      {draft => (
        <ProfileEditor
          initial={draft}
          libraryWorkerId={props.libraryWorkerId}
          computers={props.computers}
          {...(props.availableProviders !== undefined ? { availableProviders: props.availableProviders } : {})}
          onSaved={() => {
            setEditing(undefined)
            props.onChanged()
          }}
          onCancel={() => setEditing(undefined)}
        />
      )}
    </Show>
  )
}

export function ProfileEditor(props: {
  initial: ProfileDraft
  libraryWorkerId: string
  computers: readonly WorkflowComputer[]
  availableProviders?: AgentProvider[]
  onSaved: (profile: AgentProfile) => void
  onCancel: () => void
}) {
  // The draft is copied once; the editor owns it from here.
  const [draft, setDraft] = createStore<ProfileDraft>(structuredClone(props.initial))
  const save = createWorkflowAction()
  const provider = () => (draft.provider === AgentProvider.UNSPECIFIED ? undefined : draft.provider)

  const submit = (e: Event) => {
    e.preventDefault()
    void save.run(async () => {
      if (draft.provider === AgentProvider.UNSPECIFIED)
        throw new Error('Choose a provider for this profile.')
      const resp = await workerRpc.saveAgentProfile(props.libraryWorkerId, { profile: profileInit(draft) })
      if (resp.profile)
        props.onSaved(resp.profile)
    })
  }

  return (
    <form class={styles.form} onSubmit={submit} aria-label="Agent profile" data-testid="workflow-profile-editor">
      <h3 class={styles.sectionTitle}>{draft.id ? `Edit ${props.initial.name || 'profile'}` : 'New profile'}</h3>
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
      <AgentProviderSelector
        value={provider}
        onChange={(next) => {
          // Another provider's option ids mean nothing to this one.
          if (next !== draft.provider)
            setDraft('options', 'rest', {})
          setDraft('provider', next)
        }}
        {...(props.availableProviders !== undefined ? { availableProviders: props.availableProviders } : {})}
      />
      <OptionFieldsEditor
        provider={provider()}
        value={draft.options}
        onChange={next => setDraft('options', next)}
        showPermissionMode
        idPrefix="profile"
      />
      <fieldset class={styles.fieldset}>
        <legend>Placement</legend>
        <PlacementEditor
          value={draft.placement}
          onChange={next => setDraft('placement', next)}
          computers={props.computers}
          idPrefix="profile"
        />
      </fieldset>
      <div class={styles.grid2}>
        <label class={styles.field}>
          Working directory
          <input type="text" placeholder="The run's project directory" value={draft.workingDir} onInput={e => setDraft('workingDir', e.currentTarget.value)} />
        </label>
        <label class={styles.field}>
          Max instances
          <input type="number" min={1} value={draft.maxInstances} onInput={e => setDraft('maxInstances', e.currentTarget.valueAsNumber || 1)} />
        </label>
      </div>
      <label class={styles.field}>
        Role instructions (Markdown)
        <textarea class={styles.textarea} value={draft.instructions} onInput={e => setDraft('instructions', e.currentTarget.value)} />
      </label>
      <Show when={save.error()}>
        {message => <Alert variant="danger">{message()}</Alert>}
      </Show>
      <div class={styles.footer}>
        <button type="button" class="outline" onClick={() => props.onCancel()} disabled={save.busy()}>Cancel</button>
        <button type="submit" disabled={save.busy()}>{save.busy() ? 'Saving...' : 'Save profile'}</button>
      </div>
    </form>
  )
}
