import type { AgentProfile, WorkflowTemplate } from '~/generated/proto/leapmux/v1/workflow_pb'
import { createSignal, For, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Alert } from '~/components/common/Alert'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { showInfoToast } from '~/components/common/Toast'
import { WorkflowExportFormat } from '~/generated/proto/leapmux/v1/workflow_pb'
import { duplicateTemplateDraft, exportFileName, missingProfileRoles, templateInit } from '~/stores/workflows'
import { createWorkflowAction } from './createWorkflowAction'
import * as styles from './Workflows.css'

/** Hands a text document to the browser as a download. */
export function downloadTextFile(fileName: string, text: string, type: string): void {
  const url = URL.createObjectURL(new Blob([text], { type }))
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = fileName
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  }
  finally {
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
}

export interface WorkflowTemplatesProps {
  libraryWorkerId: string
  templates: readonly WorkflowTemplate[]
  profiles: readonly AgentProfile[]
  onChanged: () => void
  onNew: () => void
  onEdit: (template: WorkflowTemplate) => void
  onLaunch: (template: WorkflowTemplate) => void
}

export function WorkflowTemplates(props: WorkflowTemplatesProps) {
  const action = createWorkflowAction()
  const [importing, setImporting] = createSignal(false)
  const [importText, setImportText] = createSignal('')

  const exportTemplate = (template: WorkflowTemplate, format: WorkflowExportFormat) => void action.run(async () => {
    const json = format === WorkflowExportFormat.JSON
    const resp = await workerRpc.exportWorkflowTemplate(props.libraryWorkerId, { templateId: template.id, format })
    downloadTextFile(
      exportFileName(resp.fileName, template.name, json),
      resp.document,
      json ? 'application/json' : 'application/yaml',
    )
  })

  // A copy is saved at once, with no id (so the library mints one) and a name
  // that says it is a copy; the user edits it from the list like any other.
  const duplicate = (template: WorkflowTemplate) => void action.run(async () => {
    const copy = duplicateTemplateDraft(template, props.templates.map(t => t.name))
    await workerRpc.saveWorkflowTemplate(props.libraryWorkerId, { template: templateInit(copy) })
    showInfoToast(`Saved "${copy.name}".`)
    props.onChanged()
  })

  const importDocument = () => void action.run(async () => {
    const doc = importText().trim()
    if (!doc)
      throw new Error('Paste a YAML or JSON workflow, or choose a file.')
    const resp = await workerRpc.importWorkflowTemplate(props.libraryWorkerId, { document: doc })
    setImporting(false)
    setImportText('')
    showInfoToast(`Imported "${resp.template?.name ?? 'workflow'}" with ${resp.profiles.length} profile${resp.profiles.length === 1 ? '' : 's'}.`)
    props.onChanged()
  })

  return (
    <div class={styles.form}>
      <div class={styles.sectionHeader}>
        <p class={styles.description}>A template is a team (roles) and a default process (steps) the Lead follows.</p>
        <div class={styles.actions}>
          <button
            type="button"
            class="outline"
            disabled={!props.libraryWorkerId || action.busy()}
            onClick={() => void action.run(async () => {
              const resp = await workerRpc.installExampleWorkflow(props.libraryWorkerId, {})
              showInfoToast(`Added "${resp.template?.name ?? 'the example workflow'}".`)
              props.onChanged()
            })}
          >
            Add example
          </button>
          <button type="button" class="outline" disabled={!props.libraryWorkerId} onClick={() => setImporting(v => !v)}>Import...</button>
          <button type="button" disabled={!props.libraryWorkerId} onClick={() => props.onNew()}>New template</button>
        </div>
      </div>

      <Show when={importing()}>
        <div class={styles.card} data-testid="workflow-import">
          <label class={styles.field}>
            Workflow document (YAML or JSON)
            <textarea class={styles.textarea} value={importText()} onInput={e => setImportText(e.currentTarget.value)} />
          </label>
          <div class={styles.inline}>
            <label class={styles.field}>
              Or choose a file
              <input
                type="file"
                accept=".yaml,.yml,.json,application/json,application/yaml,text/yaml"
                onChange={(e) => {
                  const file = e.currentTarget.files?.[0]
                  if (file)
                    void file.text().then(setImportText)
                }}
              />
            </label>
          </div>
          <p class={styles.hint}>Importing always creates a new template and new profiles.</p>
          <div class={styles.actions}>
            <button type="button" class="outline" onClick={() => setImporting(false)}>Cancel</button>
            <button type="button" disabled={action.busy()} onClick={importDocument}>Import</button>
          </div>
        </div>
      </Show>

      <Show when={action.error()}>
        {message => <Alert variant="danger">{message()}</Alert>}
      </Show>

      <Show
        when={props.templates.length > 0}
        fallback={(
          <div class={styles.empty}>
            <strong>No templates yet</strong>
            <p>Create one, import one, or add the example "Develop, audit and test on iOS" workflow.</p>
          </div>
        )}
      >
        <ul class={styles.list} aria-label="Workflow templates">
          <For each={props.templates}>
            {(template) => {
              const missing = () => missingProfileRoles(template, props.profiles)
              return (
                <li class={styles.row} data-testid="workflow-template-row">
                  <div class={styles.identity}>
                    <h3 class={styles.title}>{template.name}</h3>
                    <Show when={template.description}>
                      <span class={styles.secondary}>{template.description}</span>
                    </Show>
                    <span class={styles.secondary}>
                      {`${template.roles.length} role${template.roles.length === 1 ? '' : 's'} · ${template.steps.length} step${template.steps.length === 1 ? '' : 's'} · version ${template.version}`}
                    </span>
                    <Show when={missing().length > 0}>
                      <span class={styles.secondary} role="status">{`Missing profile for: ${missing().join(', ')}`}</span>
                    </Show>
                  </div>
                  <div class={styles.actions}>
                    <button type="button" class="small" onClick={() => props.onLaunch(template)}>Launch</button>
                    <button type="button" class="outline small" onClick={() => props.onEdit(template)}>Edit</button>
                    <button type="button" class="outline small" disabled={action.busy()} onClick={() => duplicate(template)}>Duplicate</button>
                    <button type="button" class="outline small" disabled={action.busy()} onClick={() => exportTemplate(template, WorkflowExportFormat.YAML)}>Export YAML</button>
                    <button type="button" class="outline small" disabled={action.busy()} onClick={() => exportTemplate(template, WorkflowExportFormat.JSON)}>Export JSON</button>
                    <ConfirmButton
                      class="outline small"
                      data-variant="danger"
                      confirmLabel="Delete?"
                      disabled={action.busy()}
                      onClick={() => void action.run(async () => {
                        await workerRpc.deleteWorkflowTemplate(props.libraryWorkerId, { templateId: template.id })
                        props.onChanged()
                      })}
                    >
                      Delete
                    </ConfirmButton>
                  </div>
                </li>
              )
            }}
          </For>
        </ul>
      </Show>
    </div>
  )
}
