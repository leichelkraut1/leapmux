/**
 * Pure helpers for the Workflows UI: the label and tone of every workflow
 * enum, the run list aggregated across computers, the editable drafts of a
 * profile and a template, and the decisions the launch form makes (which
 * computer coordinates, which overrides ride the request).
 *
 * Nothing here calls a worker or touches a signal, so every rule the UI shows
 * is testable without rendering it. The vocabulary is the proto enums'
 * (`workflow.proto`); a renumber reaches these tables through the generated
 * enum, and a new value is a compile error in each `Record` below.
 */

import type { MessageInitShape } from '@bufbuild/protobuf'
import type {
  AgentProfile,
  AgentProfileSchema,
  WorkerPlacement,
  WorkflowRole,
  WorkflowRoleOverrideSchema,
  WorkflowRunSummary,
  WorkflowStep,
  WorkflowTemplate,
  WorkflowTemplateSchema,
} from '~/generated/proto/leapmux/v1/workflow_pb'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  WorkerPlacementStrategy,
  WorkflowAgentStatus,
  WorkflowApprovalGate,
  WorkflowApprovalKind,
  WorkflowApprovalStatus,
  WorkflowEventKind,
  WorkflowGitIsolation,
  WorkflowRunStatus,
  WorkflowTaskOutcome,
  WorkflowTaskStatus,
} from '~/generated/proto/leapmux/v1/workflow_pb'

// ---------------------------------------------------------------------------
// Labels and tones
// ---------------------------------------------------------------------------

/**
 * How a status reads at a glance. The UI maps each tone to a badge variant;
 * `neutral` is the default badge and `muted` the secondary one.
 */
export type WorkflowTone = 'success' | 'warning' | 'danger' | 'neutral' | 'muted'

export interface StatusView {
  label: string
  tone: WorkflowTone
}

const UNKNOWN: StatusView = { label: 'Unknown', tone: 'muted' }

const RUN_STATUS: Record<WorkflowRunStatus, StatusView> = {
  [WorkflowRunStatus.UNSPECIFIED]: UNKNOWN,
  [WorkflowRunStatus.PENDING]: { label: 'Pending', tone: 'muted' },
  [WorkflowRunStatus.RUNNING]: { label: 'Running', tone: 'neutral' },
  [WorkflowRunStatus.WAITING]: { label: 'Waiting on you', tone: 'warning' },
  [WorkflowRunStatus.BLOCKED]: { label: 'Blocked', tone: 'danger' },
  [WorkflowRunStatus.PAUSED]: { label: 'Paused', tone: 'warning' },
  [WorkflowRunStatus.FAILED]: { label: 'Failed', tone: 'danger' },
  [WorkflowRunStatus.CANCELLED]: { label: 'Cancelled', tone: 'muted' },
  [WorkflowRunStatus.COMPLETED]: { label: 'Completed', tone: 'success' },
}

const TASK_STATUS: Record<WorkflowTaskStatus, StatusView> = {
  [WorkflowTaskStatus.UNSPECIFIED]: UNKNOWN,
  [WorkflowTaskStatus.PENDING]: { label: 'Planned', tone: 'muted' },
  [WorkflowTaskStatus.QUEUED]: { label: 'Queued', tone: 'muted' },
  [WorkflowTaskStatus.AWAITING_APPROVAL]: { label: 'Awaiting approval', tone: 'warning' },
  [WorkflowTaskStatus.DISPATCHING]: { label: 'Dispatching', tone: 'neutral' },
  [WorkflowTaskStatus.RUNNING]: { label: 'Running', tone: 'neutral' },
  [WorkflowTaskStatus.SUBMITTED]: { label: 'In review', tone: 'neutral' },
  [WorkflowTaskStatus.BLOCKED]: { label: 'Blocked', tone: 'danger' },
  [WorkflowTaskStatus.SUCCEEDED]: { label: 'Succeeded', tone: 'success' },
  [WorkflowTaskStatus.FAILED]: { label: 'Failed', tone: 'danger' },
  [WorkflowTaskStatus.CANCELLED]: { label: 'Cancelled', tone: 'muted' },
}

const AGENT_STATUS: Record<WorkflowAgentStatus, StatusView> = {
  [WorkflowAgentStatus.UNSPECIFIED]: UNKNOWN,
  [WorkflowAgentStatus.STARTING]: { label: 'Starting', tone: 'muted' },
  [WorkflowAgentStatus.ACTIVE]: { label: 'Active', tone: 'success' },
  [WorkflowAgentStatus.UNREACHABLE]: { label: 'Unreachable', tone: 'warning' },
  [WorkflowAgentStatus.LOST]: { label: 'Lost', tone: 'danger' },
  [WorkflowAgentStatus.CLOSED]: { label: 'Closed', tone: 'muted' },
}

const APPROVAL_STATUS: Record<WorkflowApprovalStatus, StatusView> = {
  [WorkflowApprovalStatus.UNSPECIFIED]: UNKNOWN,
  [WorkflowApprovalStatus.PENDING]: { label: 'Pending', tone: 'warning' },
  [WorkflowApprovalStatus.APPROVED]: { label: 'Approved', tone: 'success' },
  [WorkflowApprovalStatus.REJECTED]: { label: 'Rejected', tone: 'danger' },
  [WorkflowApprovalStatus.CANCELLED]: { label: 'Cancelled', tone: 'muted' },
}

const APPROVAL_KIND: Record<WorkflowApprovalKind, string> = {
  [WorkflowApprovalKind.UNSPECIFIED]: 'Approval',
  [WorkflowApprovalKind.TASK_START]: 'Start a task',
  [WorkflowApprovalKind.TASK_COMPLETION]: 'Accept a result',
  [WorkflowApprovalKind.RUN_COMPLETION]: 'Finish the run',
  [WorkflowApprovalKind.ESCALATION]: 'One more attempt',
  [WorkflowApprovalKind.OPERATION]: 'Sensitive operation',
}

const EVENT_KIND: Record<WorkflowEventKind, string> = {
  [WorkflowEventKind.UNSPECIFIED]: 'Event',
  [WorkflowEventKind.RUN]: 'Run',
  [WorkflowEventKind.TASK]: 'Task',
  [WorkflowEventKind.AGENT]: 'Agent',
  [WorkflowEventKind.MESSAGE]: 'Message',
  [WorkflowEventKind.RESULT]: 'Result',
  [WorkflowEventKind.REVIEW]: 'Review',
  [WorkflowEventKind.APPROVAL]: 'Approval',
  [WorkflowEventKind.PLACEMENT]: 'Placement',
  [WorkflowEventKind.ERROR]: 'Error',
  [WorkflowEventKind.USER]: 'You',
}

const TASK_OUTCOME: Record<WorkflowTaskOutcome, StatusView> = {
  [WorkflowTaskOutcome.UNSPECIFIED]: UNKNOWN,
  [WorkflowTaskOutcome.COMPLETED]: { label: 'Completed', tone: 'success' },
  [WorkflowTaskOutcome.FAILED]: { label: 'Failed', tone: 'danger' },
  [WorkflowTaskOutcome.BLOCKED]: { label: 'Blocked', tone: 'warning' },
}

export const APPROVAL_GATE_LABELS: Record<WorkflowApprovalGate, string> = {
  [WorkflowApprovalGate.UNSPECIFIED]: 'None',
  [WorkflowApprovalGate.NONE]: 'None',
  [WorkflowApprovalGate.BEFORE_START]: 'Before start',
  [WorkflowApprovalGate.BEFORE_COMPLETE]: 'Before complete',
}

export const GIT_ISOLATION_LABELS: Record<WorkflowGitIsolation, string> = {
  [WorkflowGitIsolation.UNSPECIFIED]: 'Shared',
  [WorkflowGitIsolation.SHARED]: 'Shared',
  [WorkflowGitIsolation.WORKTREE]: 'Worktree',
}

export const PLACEMENT_STRATEGY_LABELS: Record<WorkerPlacementStrategy, string> = {
  [WorkerPlacementStrategy.UNSPECIFIED]: 'Any',
  [WorkerPlacementStrategy.SPECIFIC_WORKER]: 'Specific computer',
  [WorkerPlacementStrategy.CAPABILITY]: 'Capability',
  [WorkerPlacementStrategy.ANY_WORKER]: 'Any',
}

export function runStatusView(status: WorkflowRunStatus): StatusView {
  return RUN_STATUS[status] ?? UNKNOWN
}

export function taskStatusView(status: WorkflowTaskStatus): StatusView {
  return TASK_STATUS[status] ?? UNKNOWN
}

export function agentStatusView(status: WorkflowAgentStatus): StatusView {
  return AGENT_STATUS[status] ?? UNKNOWN
}

export function approvalStatusView(status: WorkflowApprovalStatus): StatusView {
  return APPROVAL_STATUS[status] ?? UNKNOWN
}

export function approvalKindLabel(kind: WorkflowApprovalKind): string {
  return APPROVAL_KIND[kind] ?? 'Approval'
}

export function eventKindLabel(kind: WorkflowEventKind): string {
  return EVENT_KIND[kind] ?? 'Event'
}

export function taskOutcomeView(outcome: WorkflowTaskOutcome): StatusView {
  return TASK_OUTCOME[outcome] ?? UNKNOWN
}

/** The oat badge variant for a tone; `undefined` is the default badge. */
export function toneBadgeVariant(tone: WorkflowTone): 'success' | 'warning' | 'danger' | 'secondary' | undefined {
  switch (tone) {
    case 'success':
    case 'warning':
    case 'danger':
      return tone
    case 'muted':
      return 'secondary'
    case 'neutral':
      return undefined
  }
}

// ---------------------------------------------------------------------------
// What a run or a task allows (the engine's own rules, stated for the buttons)
// ---------------------------------------------------------------------------

export function isRunFinal(status: WorkflowRunStatus): boolean {
  return status === WorkflowRunStatus.FAILED
    || status === WorkflowRunStatus.CANCELLED
    || status === WorkflowRunStatus.COMPLETED
}

export function canPauseRun(status: WorkflowRunStatus): boolean {
  return status === WorkflowRunStatus.RUNNING || status === WorkflowRunStatus.WAITING
}

export function canResumeRun(status: WorkflowRunStatus): boolean {
  return status === WorkflowRunStatus.PAUSED || status === WorkflowRunStatus.BLOCKED
}

export function canCancelRun(status: WorkflowRunStatus): boolean {
  return !isRunFinal(status)
}

/** A run is deleted only once it finished, or before its Lead attached. */
export function canDeleteRun(status: WorkflowRunStatus): boolean {
  return isRunFinal(status) || status === WorkflowRunStatus.PENDING
}

export function isTaskFinal(status: WorkflowTaskStatus): boolean {
  return status === WorkflowTaskStatus.SUCCEEDED
    || status === WorkflowTaskStatus.FAILED
    || status === WorkflowTaskStatus.CANCELLED
}

export function canRetryTask(status: WorkflowTaskStatus): boolean {
  return status === WorkflowTaskStatus.BLOCKED
    || status === WorkflowTaskStatus.FAILED
    || status === WorkflowTaskStatus.CANCELLED
}

export function canCancelTask(status: WorkflowTaskStatus): boolean {
  return !isTaskFinal(status)
}

/** Anything but a success can move to another computer. */
export function canReassignTask(status: WorkflowTaskStatus): boolean {
  return status !== WorkflowTaskStatus.SUCCEEDED
}

// ---------------------------------------------------------------------------
// Computers
// ---------------------------------------------------------------------------

/** One of the user's workers as the Workflows UI shows it. */
export interface WorkflowComputer {
  id: string
  name: string
  online: boolean
  os: string
  /** Undefined until the computer's system info arrives. */
  capabilities?: readonly string[]
}

export function computerName(computers: readonly WorkflowComputer[], workerId: string): string {
  if (!workerId)
    return 'No computer'
  return computers.find(c => c.id === workerId)?.name || workerId
}

/** Name, online state and OS in one line, for a menu row or a summary. */
export function computerDescription(computer: WorkflowComputer): string {
  const parts = [computer.online ? 'Online' : 'Offline']
  if (computer.os)
    parts.push(computer.os)
  return parts.join(' · ')
}

/** The first online computer, then the first one at all: the default library computer. */
export function defaultComputerId(computers: readonly WorkflowComputer[]): string {
  return computers.find(c => c.online)?.id ?? computers[0]?.id ?? ''
}

export type PlacementFit = 'fits' | 'excluded' | 'unknown'

/**
 * Whether a computer satisfies a placement, as far as the browser knows. A
 * capability placement over a computer whose capabilities have not arrived is
 * `unknown`, never `excluded`: the coordinator decides.
 */
export function placementFit(placement: PlacementLike | undefined, computer: WorkflowComputer): PlacementFit {
  const strategy = placement?.strategy ?? WorkerPlacementStrategy.ANY_WORKER
  if (strategy === WorkerPlacementStrategy.SPECIFIC_WORKER)
    return placement?.workerId === computer.id ? 'fits' : 'excluded'
  const allowed = placement?.allowedWorkerIds ?? []
  if (allowed.length > 0 && !allowed.includes(computer.id))
    return 'excluded'
  if (strategy === WorkerPlacementStrategy.CAPABILITY) {
    if (computer.capabilities === undefined)
      return 'unknown'
    const have = new Set(computer.capabilities.map(c => c.toLowerCase()))
    return (placement?.requiredCapabilities ?? []).every(c => have.has(c.toLowerCase())) ? 'fits' : 'excluded'
  }
  return 'fits'
}

// ---------------------------------------------------------------------------
// Placement
// ---------------------------------------------------------------------------

/** The placement fields every reader needs, from a message or a draft. */
export type PlacementLike = Pick<WorkerPlacement, 'strategy' | 'workerId' | 'requiredCapabilities' | 'allowedWorkerIds'>

export interface PlacementDraft {
  strategy: WorkerPlacementStrategy
  workerId: string
  requiredCapabilities: string[]
  allowedWorkerIds: string[]
}

export function placementDraft(placement?: PlacementLike): PlacementDraft {
  const strategy = placement?.strategy ?? WorkerPlacementStrategy.UNSPECIFIED
  return {
    strategy: strategy === WorkerPlacementStrategy.UNSPECIFIED ? WorkerPlacementStrategy.ANY_WORKER : strategy,
    workerId: placement?.workerId ?? '',
    requiredCapabilities: [...(placement?.requiredCapabilities ?? [])],
    allowedWorkerIds: [...(placement?.allowedWorkerIds ?? [])],
  }
}

/** The placement as the wire takes it: only the fields its strategy reads. */
export function placementInit(draft: PlacementDraft): PlacementDraft {
  switch (draft.strategy) {
    case WorkerPlacementStrategy.SPECIFIC_WORKER:
      return { strategy: draft.strategy, workerId: draft.workerId.trim(), requiredCapabilities: [], allowedWorkerIds: [] }
    case WorkerPlacementStrategy.CAPABILITY:
      return { strategy: draft.strategy, workerId: '', requiredCapabilities: normalizeCapabilities(draft.requiredCapabilities), allowedWorkerIds: [...draft.allowedWorkerIds] }
    default:
      return { strategy: WorkerPlacementStrategy.ANY_WORKER, workerId: '', requiredCapabilities: [], allowedWorkerIds: [...draft.allowedWorkerIds] }
  }
}

/** Lowercased, trimmed, deduplicated and sorted, as the engine stores them. */
export function normalizeCapabilities(values: readonly string[]): string[] {
  return [...new Set(values.map(v => v.trim().toLowerCase()).filter(v => v !== ''))].sort()
}

/** Splits "os:darwin, xcode gpu" into capability entries. */
export function parseCapabilityInput(text: string): string[] {
  return normalizeCapabilities(text.split(/[\s,]+/))
}

/** One sentence that says where a role's agents run. */
export function placementSummary(placement: PlacementLike | undefined, nameOf: (workerId: string) => string): string {
  const strategy = placement?.strategy ?? WorkerPlacementStrategy.ANY_WORKER
  const allowed = placement?.allowedWorkerIds ?? []
  const among = allowed.length > 0 ? ` among ${allowed.map(nameOf).join(', ')}` : ''
  switch (strategy) {
    case WorkerPlacementStrategy.SPECIFIC_WORKER:
      return `On ${nameOf(placement?.workerId ?? '')}`
    case WorkerPlacementStrategy.CAPABILITY:
      return `Any online computer with ${(placement?.requiredCapabilities ?? []).join(', ') || 'no capability'}${among}`
    default:
      return `Any online computer${among}`
  }
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

export const OPTION_MODEL = 'model'
export const OPTION_EFFORT = 'effort'
export const OPTION_PERMISSION_MODE = 'permissionMode'

export interface OptionFields {
  model: string
  effort: string
  permissionMode: string
  /** Every other option id (provider axes), kept as the map had it. */
  rest: Record<string, string>
}

export function splitOptions(options: Readonly<Record<string, string>> | undefined): OptionFields {
  const { [OPTION_MODEL]: model = '', [OPTION_EFFORT]: effort = '', [OPTION_PERMISSION_MODE]: permissionMode = '', ...rest } = options ?? {}
  return { model, effort, permissionMode, rest }
}

/** The map the wire takes. A blank field is omitted, so the provider default applies. */
export function joinOptions(fields: OptionFields): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(fields.rest)) {
    if (key.trim() && value.trim())
      out[key.trim()] = value.trim()
  }
  if (fields.model.trim())
    out[OPTION_MODEL] = fields.model.trim()
  if (fields.effort.trim())
    out[OPTION_EFFORT] = fields.effort.trim()
  if (fields.permissionMode.trim())
    out[OPTION_PERMISSION_MODE] = fields.permissionMode.trim()
  return out
}

/** "model, effort" for a role row; empty when both are the provider default. */
export function modelSummary(options: Readonly<Record<string, string>> | undefined): string {
  const { model, effort } = splitOptions(options)
  return [model || 'default model', effort ? `${effort} effort` : ''].filter(Boolean).join(', ')
}

// ---------------------------------------------------------------------------
// Resolving a role (mirrors the engine's mergeAgent)
// ---------------------------------------------------------------------------

export interface ResolvedRoleAgent {
  provider: AgentProvider
  options: Record<string, string>
  placement: PlacementDraft
  workingDir: string
}

type AgentFields = Pick<AgentProfile, 'agentProvider' | 'options' | 'placement' | 'workingDir'>

function mergeAgentFields(base: ResolvedRoleAgent, over: Partial<AgentFields> | undefined): ResolvedRoleAgent {
  if (!over)
    return base
  const out: ResolvedRoleAgent = { ...base, options: { ...base.options }, placement: { ...base.placement } }
  const provider = over.agentProvider ?? AgentProvider.UNSPECIFIED
  if (provider !== AgentProvider.UNSPECIFIED) {
    // Another provider's option ids mean nothing to this one.
    if (provider !== out.provider)
      out.options = {}
    out.provider = provider
  }
  for (const [key, value] of Object.entries(over.options ?? {}))
    out.options[key] = value
  if (over.placement)
    out.placement = placementDraft(over.placement)
  if (over.workingDir)
    out.workingDir = over.workingDir
  return out
}

/**
 * A template role's effective agent before launch: its profile, then the
 * role's own override, then (optionally) a launch override. The same order
 * `ResolveRoles` applies on the coordinator, so the launch form shows what the
 * run will record.
 */
export function resolveRoleAgent(
  role: Pick<WorkflowRole, 'profileId' | 'agent'>,
  profiles: readonly AgentProfile[],
  launch?: Partial<AgentFields>,
): ResolvedRoleAgent {
  const empty: ResolvedRoleAgent = {
    provider: AgentProvider.UNSPECIFIED,
    options: {},
    placement: placementDraft(),
    workingDir: '',
  }
  const profile = role.profileId ? profiles.find(p => p.id === role.profileId) : undefined
  return mergeAgentFields(mergeAgentFields(mergeAgentFields(empty, profile), role.agent), launch)
}

export function leadRoleOf<R extends Pick<WorkflowRole, 'isLead'>>(roles: readonly R[]): R | undefined {
  return roles.find(r => r.isLead)
}

/** The profiles a template's roles reference, in role order, each once. */
export function referencedProfiles(template: Pick<WorkflowTemplate, 'roles'>, profiles: readonly AgentProfile[]): AgentProfile[] {
  const out: AgentProfile[] = []
  for (const role of template.roles) {
    const profile = profiles.find(p => p.id === role.profileId)
    if (profile && !out.includes(profile))
      out.push(profile)
  }
  return out
}

/** Role keys whose profile is missing from the library: the launch cannot resolve them. */
export function missingProfileRoles(template: Pick<WorkflowTemplate, 'roles'>, profiles: readonly AgentProfile[]): string[] {
  return template.roles
    .filter(r => r.profileId && !profiles.some(p => p.id === r.profileId))
    .map(r => r.key)
}

// ---------------------------------------------------------------------------
// Launch decisions
// ---------------------------------------------------------------------------

export type CoordinatorChoice
  = | { pinned: true, workerId: string }
    | { pinned: false, workerId: string }

/**
 * The run's coordinator is the computer the Lead runs on. A Lead pinned to a
 * computer decides it; otherwise the user's pick does (empty until they make
 * one).
 */
export function decideCoordinator(leadPlacement: PlacementLike | undefined, chosenWorkerId: string): CoordinatorChoice {
  if (leadPlacement?.strategy === WorkerPlacementStrategy.SPECIFIC_WORKER && leadPlacement.workerId)
    return { pinned: true, workerId: leadPlacement.workerId }
  return { pinned: false, workerId: chosenWorkerId }
}

/** The default Lead computer: the first online computer that fits, then any online one. */
export function defaultLeadComputer(leadPlacement: PlacementLike | undefined, computers: readonly WorkflowComputer[]): string {
  const online = computers.filter(c => c.online)
  return online.find(c => placementFit(leadPlacement, c) === 'fits')?.id
    ?? online.find(c => placementFit(leadPlacement, c) === 'unknown')?.id
    ?? ''
}

/**
 * The pinned computer of a placement when it is offline or unknown. The engine
 * waits for it, so the UI warns and never substitutes another.
 */
export function offlinePinnedComputer(placement: PlacementLike | undefined, computers: readonly WorkflowComputer[]): string | undefined {
  if (placement?.strategy !== WorkerPlacementStrategy.SPECIFIC_WORKER || !placement.workerId)
    return undefined
  const computer = computers.find(c => c.id === placement.workerId)
  return computer?.online ? undefined : placement.workerId
}

/** What the user changed for one role in the launch form. Blank keeps the template's. */
export interface RoleLaunchDraft {
  roleKey: string
  /** A computer to pin the role to, or '' to keep the template's placement. */
  workerId: string
  model: string
  effort: string
}

export type RoleOverrideInit = MessageInitShape<typeof WorkflowRoleOverrideSchema>

/**
 * The launch overrides, one per role the user changed. A Lead never carries a
 * placement: it runs on the coordinator, which `decideCoordinator` chose.
 */
export function buildLaunchOverrides(drafts: readonly RoleLaunchDraft[], leadRoleKey: string): RoleOverrideInit[] {
  const out: RoleOverrideInit[] = []
  for (const draft of drafts) {
    const options = joinOptions({ model: draft.model, effort: draft.effort, permissionMode: '', rest: {} })
    const workerId = draft.roleKey === leadRoleKey ? '' : draft.workerId.trim()
    if (!workerId && Object.keys(options).length === 0)
      continue
    out.push({
      roleKey: draft.roleKey,
      ...(workerId ? { placement: { strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId } } : {}),
      ...(Object.keys(options).length > 0 ? { options } : {}),
    })
  }
  return out
}

/** The override a running run's role takes from the panel's editor. */
export function buildRoleUpdate(draft: RoleLaunchDraft, isLead: boolean): RoleOverrideInit {
  const [override] = buildLaunchOverrides([draft], isLead ? draft.roleKey : '')
  return override ?? { roleKey: draft.roleKey }
}

// ---------------------------------------------------------------------------
// The run panel's search param
// ---------------------------------------------------------------------------

export interface RunRef {
  coordinatorWorkerId: string
  runId: string
}

/** `<coordinatorWorkerId>:<runId>`. */
export function formatRunParam(ref: RunRef): string {
  return `${ref.coordinatorWorkerId}:${ref.runId}`
}

export function parseRunParam(value: string | string[] | undefined): RunRef | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  if (!raw)
    return undefined
  // The LAST colon: a run id is generated without one, a worker id might not be.
  const at = raw.lastIndexOf(':')
  if (at <= 0 || at === raw.length - 1)
    return undefined
  return { coordinatorWorkerId: raw.slice(0, at), runId: raw.slice(at + 1) }
}

// ---------------------------------------------------------------------------
// Runs across computers
// ---------------------------------------------------------------------------

export interface RunRow {
  key: string
  coordinatorWorkerId: string
  summary: WorkflowRunSummary
}

/**
 * Every computer's runs as one list, newest activity first. A run lives on its
 * coordinator alone, so the coordinator that answered is the one to address.
 */
export function aggregateRuns(results: readonly { workerId: string, runs: readonly WorkflowRunSummary[] }[]): RunRow[] {
  const byKey = new Map<string, RunRow>()
  for (const result of results) {
    for (const summary of result.runs) {
      const run = summary.run
      if (!run?.id)
        continue
      const coordinatorWorkerId = run.coordinatorWorkerId || result.workerId
      const key = formatRunParam({ coordinatorWorkerId, runId: run.id })
      if (!byKey.has(key))
        byKey.set(key, { key, coordinatorWorkerId, summary })
    }
  }
  const updated = (row: RunRow) => Number(row.summary.run?.updatedAtMs ?? 0n)
  return [...byKey.values()].sort((a, b) => updated(b) - updated(a))
}

// ---------------------------------------------------------------------------
// Editable drafts
// ---------------------------------------------------------------------------

export interface ProfileDraft {
  id: string
  name: string
  description: string
  provider: AgentProvider
  options: OptionFields
  placement: PlacementDraft
  workingDir: string
  instructions: string
  maxInstances: number
}

export function profileDraft(profile?: AgentProfile): ProfileDraft {
  return {
    id: profile?.id ?? '',
    name: profile?.name ?? '',
    description: profile?.description ?? '',
    provider: profile?.agentProvider ?? AgentProvider.UNSPECIFIED,
    options: splitOptions(profile?.options),
    placement: placementDraft(profile?.placement),
    workingDir: profile?.workingDir ?? '',
    instructions: profile?.instructions ?? '',
    maxInstances: profile?.maxInstances || 1,
  }
}

export function profileInit(draft: ProfileDraft): MessageInitShape<typeof AgentProfileSchema> {
  return {
    id: draft.id,
    name: draft.name.trim(),
    description: draft.description.trim(),
    agentProvider: draft.provider,
    options: joinOptions(draft.options),
    placement: placementInit(draft.placement),
    workingDir: draft.workingDir.trim(),
    instructions: draft.instructions,
    maxInstances: Math.max(0, Math.floor(draft.maxInstances || 0)),
  }
}

export const REQUIRED_OUTPUTS = [
  { key: 'summary', label: 'Summary' },
  { key: 'commit', label: 'Commit' },
  { key: 'tests', label: 'Tests' },
  { key: 'artifacts', label: 'Artifacts' },
] as const

export interface RoleDraft {
  key: string
  name: string
  profileId: string
  isLead: boolean
  gitIsolation: WorkflowGitIsolation
  requiredOutputs: string[]
  reviewRequired: boolean
  /** UNSPECIFIED inherits the profile's provider. */
  provider: AgentProvider
  options: OptionFields
  /** False inherits the profile's placement. */
  overridePlacement: boolean
  placement: PlacementDraft
  /** The role-override fields this editor does not show, carried through a save. */
  workingDir: string
  instructions: string
  maxInstances: number
}

export interface StepDraft {
  key: string
  title: string
  roleKey: string
  instructions: string
  dependsOn: string[]
  approval: WorkflowApprovalGate
  maxAttempts: number
  autoStart: boolean
}

export interface TemplateDraft {
  id: string
  version: number
  name: string
  description: string
  instructions: string
  completionCriteria: string
  roles: RoleDraft[]
  steps: StepDraft[]
  defaultMaxAttempts: number
  maxParallelTasks: number
  maxAgents: number
  requireFinalApproval: boolean
  /** Limit each agent to this workflow; off keeps their full control. */
  confineAgents: boolean
  /** One per line. */
  sensitiveOperations: string
}

export function roleDraft(role?: WorkflowRole): RoleDraft {
  const agent = role?.agent
  return {
    key: role?.key ?? '',
    name: role?.name ?? '',
    profileId: role?.profileId ?? '',
    isLead: role?.isLead ?? false,
    gitIsolation: role?.gitIsolation || WorkflowGitIsolation.SHARED,
    requiredOutputs: [...(role?.requiredOutputs ?? [])],
    reviewRequired: role?.reviewRequired ?? false,
    provider: agent?.agentProvider ?? AgentProvider.UNSPECIFIED,
    options: splitOptions(agent?.options),
    overridePlacement: agent?.placement !== undefined,
    placement: placementDraft(agent?.placement),
    workingDir: agent?.workingDir ?? '',
    instructions: agent?.instructions ?? '',
    maxInstances: agent?.maxInstances ?? 0,
  }
}

export function stepDraft(step?: WorkflowStep): StepDraft {
  return {
    key: step?.key ?? '',
    title: step?.title ?? '',
    roleKey: step?.roleKey ?? '',
    instructions: step?.instructions ?? '',
    dependsOn: [...(step?.dependsOn ?? [])],
    approval: step?.approval || WorkflowApprovalGate.NONE,
    maxAttempts: step?.maxAttempts ?? 0,
    autoStart: step?.autoStart ?? false,
  }
}

export function templateDraft(template?: WorkflowTemplate): TemplateDraft {
  return {
    id: template?.id ?? '',
    version: template?.version ?? 0,
    name: template?.name ?? '',
    description: template?.description ?? '',
    instructions: template?.instructions ?? '',
    completionCriteria: template?.completionCriteria ?? '',
    roles: (template?.roles ?? []).map(roleDraft),
    steps: (template?.steps ?? []).map(stepDraft),
    defaultMaxAttempts: template?.defaultMaxAttempts ?? 0,
    maxParallelTasks: template?.maxParallelTasks ?? 0,
    maxAgents: template?.maxAgents ?? 0,
    requireFinalApproval: template?.requireFinalApproval ?? false,
    confineAgents: template?.confineAgents ?? false,
    sensitiveOperations: (template?.sensitiveOperations ?? []).join('\n'),
  }
}

function count(value: number): number {
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
}

export function templateInit(draft: TemplateDraft): MessageInitShape<typeof WorkflowTemplateSchema> {
  return {
    id: draft.id,
    version: draft.version,
    name: draft.name.trim(),
    description: draft.description.trim(),
    instructions: draft.instructions,
    completionCriteria: draft.completionCriteria,
    roles: draft.roles.map(role => ({
      key: role.key.trim(),
      name: role.name.trim(),
      profileId: role.profileId,
      isLead: role.isLead,
      gitIsolation: role.gitIsolation,
      requiredOutputs: [...role.requiredOutputs],
      reviewRequired: role.reviewRequired,
      agent: {
        agentProvider: role.provider,
        options: joinOptions(role.options),
        ...(role.overridePlacement ? { placement: placementInit(role.placement) } : {}),
        workingDir: role.workingDir.trim(),
        instructions: role.instructions,
        maxInstances: count(role.maxInstances),
      },
    })),
    steps: draft.steps.map(step => ({
      key: step.key.trim(),
      title: step.title.trim(),
      roleKey: step.roleKey,
      instructions: step.instructions,
      dependsOn: [...step.dependsOn],
      approval: step.approval,
      maxAttempts: count(step.maxAttempts),
      autoStart: step.autoStart,
    })),
    defaultMaxAttempts: count(draft.defaultMaxAttempts),
    maxParallelTasks: count(draft.maxParallelTasks),
    maxAgents: count(draft.maxAgents),
    requireFinalApproval: draft.requireFinalApproval,
    confineAgents: draft.confineAgents,
    sensitiveOperations: draft.sensitiveOperations.split('\n').map(s => s.trim()).filter(Boolean),
  }
}

/** A copy that saves as a new template: no id, no version, a name that says so. */
export function duplicateTemplateDraft(template: WorkflowTemplate, existingNames: readonly string[]): TemplateDraft {
  const draft = templateDraft(template)
  const taken = new Set(existingNames)
  let name = `${template.name} (copy)`
  for (let n = 2; taken.has(name); n++)
    name = `${template.name} (copy ${n})`
  return { ...draft, id: '', version: 0, name }
}

/** Makes `role` the only Lead. */
export function setLeadRole(roles: readonly RoleDraft[], index: number): RoleDraft[] {
  return roles.map((role, i) => ({ ...role, isLead: i === index }))
}

/** Renames a step key everywhere a dependency names it. */
export function renameStepKey(steps: readonly StepDraft[], index: number, key: string): StepDraft[] {
  const previous = steps[index]?.key ?? ''
  return steps.map((step, i) => i === index
    ? { ...step, key }
    : { ...step, dependsOn: previous ? step.dependsOn.map(dep => (dep === previous ? key : dep)) : step.dependsOn })
}

/** Removes a step and every dependency on it. */
export function removeStep(steps: readonly StepDraft[], index: number): StepDraft[] {
  const removed = steps[index]?.key ?? ''
  return steps
    .filter((_, i) => i !== index)
    .map(step => ({ ...step, dependsOn: step.dependsOn.filter(dep => dep !== removed) }))
}

/** A template-export file name with a safe fallback. */
export function exportFileName(fileName: string, templateName: string, json: boolean): string {
  if (fileName.trim())
    return fileName.trim()
  const slug = templateName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workflow'
  return `${slug}.${json ? 'json' : 'yaml'}`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** The engine's sentinel prefixes; the sentence after them is written for the user. */
const ENGINE_ERROR_PREFIXES = [
  'invalid workflow definition: ',
  'not allowed in the current state: ',
  'not permitted: ',
]

/** A refusal as the user reads it: the engine's own sentence, without its category. */
export function workflowErrorMessage(err: unknown, fallback = 'The request failed'): string {
  let message = err instanceof Error ? err.message : typeof err === 'string' ? err : ''
  for (const prefix of ENGINE_ERROR_PREFIXES) {
    const at = message.indexOf(prefix)
    if (at >= 0)
      message = message.slice(at + prefix.length)
  }
  message = message.trim()
  if (!message)
    return fallback
  if (message === 'not found')
    return 'It no longer exists on that computer.'
  return message.charAt(0).toUpperCase() + message.slice(1)
}

/** A blank template that already has its one Lead role, so it can be saved as soon as a step's role exists. */
export function newTemplateDraft(): TemplateDraft {
  return { ...templateDraft(), roles: [{ ...roleDraft(), key: 'lead', name: 'Lead', isLead: true }] }
}
