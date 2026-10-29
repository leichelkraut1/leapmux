import type { WorkflowComputer } from './workflows'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  AgentProfileSchema,
  WorkerPlacementStrategy,
  WorkflowApprovalGate,
  WorkflowRunSchema,
  WorkflowRunStatus,
  WorkflowRunSummarySchema,
  WorkflowTaskStatus,
  WorkflowTemplateSchema,
} from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  aggregateRuns,
  buildLaunchOverrides,
  buildRoleUpdate,
  canDeleteRun,
  canPauseRun,
  canResumeRun,
  canRetryTask,
  decideCoordinator,
  defaultComputerId,
  defaultLeadComputer,
  duplicateTemplateDraft,
  exportFileName,
  formatRunParam,
  joinOptions,
  missingProfileRoles,
  offlinePinnedComputer,
  parseCapabilityInput,
  parseRunParam,
  placementFit,
  placementInit,
  placementSummary,
  profileDraft,
  profileInit,
  referencedProfiles,
  removeStep,
  renameStepKey,
  resolveRoleAgent,
  runStatusView,
  setLeadRole,
  splitOptions,
  taskStatusView,
  templateDraft,
  templateInit,
  toneBadgeVariant,
  workflowErrorMessage,
} from './workflows'

const computers: WorkflowComputer[] = [
  { id: 'mac', name: 'Mac Studio', online: true, os: 'darwin', capabilities: ['os:darwin', 'xcode'] },
  { id: 'linux', name: 'Linux box', online: true, os: 'linux', capabilities: ['os:linux', 'gpu'] },
  { id: 'laptop', name: 'Laptop', online: false, os: 'windows' },
]
const nameOf = (id: string) => computers.find(c => c.id === id)?.name ?? id

const builder = create(AgentProfileSchema, {
  id: 'p-builder',
  name: 'Builder',
  agentProvider: AgentProvider.CLAUDE_CODE,
  options: { model: 'opus', effort: 'high', permissionMode: 'acceptEdits' },
  placement: { strategy: WorkerPlacementStrategy.ANY_WORKER },
})
const tester = create(AgentProfileSchema, {
  id: 'p-tester',
  name: 'iOS tester',
  agentProvider: AgentProvider.CODEX,
  placement: { strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'laptop' },
})

const template = create(WorkflowTemplateSchema, {
  id: 't-1',
  name: 'Ship it',
  version: 3,
  roles: [
    { key: 'lead', name: 'Lead', profileId: 'p-builder', isLead: true },
    { key: 'builder', name: 'Builder', profileId: 'p-builder', agent: { options: { model: 'sonnet' } } },
    { key: 'tester', name: 'Tester', profileId: 'p-tester' },
  ],
  steps: [
    { key: 'build', title: 'Build', roleKey: 'builder' },
    { key: 'test', title: 'Test', roleKey: 'tester', dependsOn: ['build'], approval: WorkflowApprovalGate.BEFORE_COMPLETE },
  ],
})

describe('runStatusView', () => {
  it('labels every run status and tones the ones that need attention', () => {
    expect(runStatusView(WorkflowRunStatus.WAITING)).toEqual({ label: 'Waiting on you', tone: 'warning' })
    expect(runStatusView(WorkflowRunStatus.COMPLETED).tone).toBe('success')
    expect(runStatusView(WorkflowRunStatus.BLOCKED).tone).toBe('danger')
    expect(runStatusView(99 as WorkflowRunStatus).label).toBe('Unknown')
  })
})

describe('taskStatusView', () => {
  it('names a submitted task as in review', () => {
    expect(taskStatusView(WorkflowTaskStatus.SUBMITTED).label).toBe('In review')
    expect(taskStatusView(WorkflowTaskStatus.AWAITING_APPROVAL).tone).toBe('warning')
  })
})

describe('toneBadgeVariant', () => {
  it('maps a tone to an oat badge variant', () => {
    expect(toneBadgeVariant('muted')).toBe('secondary')
    expect(toneBadgeVariant('neutral')).toBeUndefined()
    expect(toneBadgeVariant('danger')).toBe('danger')
  })
})

describe('run and task permissions', () => {
  it('follows the engine rules for each control', () => {
    expect(canPauseRun(WorkflowRunStatus.WAITING)).toBe(true)
    expect(canPauseRun(WorkflowRunStatus.PAUSED)).toBe(false)
    expect(canResumeRun(WorkflowRunStatus.BLOCKED)).toBe(true)
    expect(canDeleteRun(WorkflowRunStatus.PENDING)).toBe(true)
    expect(canDeleteRun(WorkflowRunStatus.RUNNING)).toBe(false)
    expect(canDeleteRun(WorkflowRunStatus.CANCELLED)).toBe(true)
    expect(canRetryTask(WorkflowTaskStatus.FAILED)).toBe(true)
    expect(canRetryTask(WorkflowTaskStatus.RUNNING)).toBe(false)
  })
})

describe('parseRunParam', () => {
  it('round-trips formatRunParam and refuses a malformed value', () => {
    const ref = { coordinatorWorkerId: 'w-1', runId: 'r-9' }
    expect(parseRunParam(formatRunParam(ref))).toEqual(ref)
    expect(parseRunParam(['a:b'])).toEqual({ coordinatorWorkerId: 'a', runId: 'b' })
    expect(parseRunParam('nocolon')).toBeUndefined()
    expect(parseRunParam(':r')).toBeUndefined()
    expect(parseRunParam('w:')).toBeUndefined()
    expect(parseRunParam(undefined)).toBeUndefined()
  })
})

describe('aggregateRuns', () => {
  it('merges every computer\'s runs, newest first, addressed to their coordinator', () => {
    const summary = (id: string, coordinator: string, updated: bigint) => create(WorkflowRunSummarySchema, {
      run: create(WorkflowRunSchema, { id, name: id, coordinatorWorkerId: coordinator, updatedAtMs: updated }),
    })
    const rows = aggregateRuns([
      { workerId: 'mac', runs: [summary('old', 'mac', 10n), summary('new', 'mac', 30n)] },
      { workerId: 'linux', runs: [summary('mid', '', 20n)] },
    ])
    expect(rows.map(r => r.key)).toEqual(['mac:new', 'linux:mid', 'mac:old'])
    // A run that does not name its coordinator belongs to the computer that answered.
    expect(rows[1]?.coordinatorWorkerId).toBe('linux')
  })

  it('drops a duplicate and a run without an id', () => {
    const one = create(WorkflowRunSummarySchema, { run: { id: 'r', coordinatorWorkerId: 'mac' } })
    const rows = aggregateRuns([
      { workerId: 'mac', runs: [one, one, create(WorkflowRunSummarySchema, {})] },
    ])
    expect(rows).toHaveLength(1)
  })
})

describe('placementSummary', () => {
  it('states each strategy in one sentence', () => {
    expect(placementSummary({ strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'mac', requiredCapabilities: [], allowedWorkerIds: [] }, nameOf))
      .toBe('On Mac Studio')
    expect(placementSummary({ strategy: WorkerPlacementStrategy.CAPABILITY, workerId: '', requiredCapabilities: ['xcode'], allowedWorkerIds: ['mac', 'linux'] }, nameOf))
      .toBe('Any online computer with xcode among Mac Studio, Linux box')
    expect(placementSummary(undefined, nameOf)).toBe('Any online computer')
  })
})

describe('placementInit', () => {
  it('sends only the fields the strategy reads', () => {
    expect(placementInit({ strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: ' mac ', requiredCapabilities: ['x'], allowedWorkerIds: ['y'] }))
      .toEqual({ strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'mac', requiredCapabilities: [], allowedWorkerIds: [] })
    expect(placementInit({ strategy: WorkerPlacementStrategy.CAPABILITY, workerId: 'mac', requiredCapabilities: ['Xcode', 'gpu', 'xcode'], allowedWorkerIds: [] }).requiredCapabilities)
      .toEqual(['gpu', 'xcode'])
  })
})

describe('parseCapabilityInput', () => {
  it('splits on commas and spaces and normalizes', () => {
    expect(parseCapabilityInput('OS:darwin, xcode  gpu,,')).toEqual(['gpu', 'os:darwin', 'xcode'])
  })
})

describe('placementFit', () => {
  it('judges capability placements only when capabilities are known', () => {
    const p = { strategy: WorkerPlacementStrategy.CAPABILITY, workerId: '', requiredCapabilities: ['xcode'], allowedWorkerIds: [] }
    expect(placementFit(p, computers[0]!)).toBe('fits')
    expect(placementFit(p, computers[1]!)).toBe('excluded')
    expect(placementFit(p, computers[2]!)).toBe('unknown')
    expect(placementFit({ ...p, strategy: WorkerPlacementStrategy.ANY_WORKER, allowedWorkerIds: ['linux'] }, computers[0]!)).toBe('excluded')
  })
})

describe('splitOptions', () => {
  it('round-trips through joinOptions, omitting blanks', () => {
    const fields = splitOptions({ model: 'opus', effort: '', thinking: 'on' })
    expect(fields).toEqual({ model: 'opus', effort: '', permissionMode: '', rest: { thinking: 'on' } })
    expect(joinOptions(fields)).toEqual({ model: 'opus', thinking: 'on' })
  })
})

describe('resolveRoleAgent', () => {
  it('applies the profile, then the role override, then the launch override', () => {
    const role = template.roles[1]!
    const resolved = resolveRoleAgent(role, [builder, tester])
    expect(resolved.provider).toBe(AgentProvider.CLAUDE_CODE)
    expect(resolved.options).toEqual({ model: 'sonnet', effort: 'high', permissionMode: 'acceptEdits' })
    const launched = resolveRoleAgent(role, [builder], { options: { effort: 'low' } })
    expect(launched.options.effort).toBe('low')
  })

  it('drops the options when an override changes the provider', () => {
    const resolved = resolveRoleAgent({ profileId: 'p-builder', agent: create(AgentProfileSchema, { agentProvider: AgentProvider.CODEX }) }, [builder])
    expect(resolved.provider).toBe(AgentProvider.CODEX)
    expect(resolved.options).toEqual({})
  })

  it('keeps a pinned profile\'s placement', () => {
    expect(resolveRoleAgent(template.roles[2]!, [builder, tester]).placement)
      .toMatchObject({ strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'laptop' })
  })
})

describe('referencedProfiles', () => {
  it('lists each referenced profile once, and reports roles whose profile is missing', () => {
    expect(referencedProfiles(template, [tester, builder]).map(p => p.id)).toEqual(['p-builder', 'p-tester'])
    expect(missingProfileRoles(template, [builder])).toEqual(['tester'])
  })
})

describe('decideCoordinator', () => {
  it('lets a pinned Lead decide and otherwise takes the user\'s pick', () => {
    expect(decideCoordinator({ strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'mac', requiredCapabilities: [], allowedWorkerIds: [] }, 'linux'))
      .toEqual({ pinned: true, workerId: 'mac' })
    expect(decideCoordinator({ strategy: WorkerPlacementStrategy.ANY_WORKER, workerId: '', requiredCapabilities: [], allowedWorkerIds: [] }, 'linux'))
      .toEqual({ pinned: false, workerId: 'linux' })
    expect(decideCoordinator(undefined, '')).toEqual({ pinned: false, workerId: '' })
  })
})

describe('defaultLeadComputer', () => {
  it('prefers an online computer that fits the Lead placement', () => {
    const gpu = { strategy: WorkerPlacementStrategy.CAPABILITY, workerId: '', requiredCapabilities: ['gpu'], allowedWorkerIds: [] }
    expect(defaultLeadComputer(gpu, computers)).toBe('linux')
    expect(defaultLeadComputer(undefined, computers)).toBe('mac')
    expect(defaultLeadComputer(undefined, [computers[2]!])).toBe('')
  })
})

describe('defaultComputerId', () => {
  it('takes the first online computer, then any', () => {
    expect(defaultComputerId([computers[2]!, computers[1]!])).toBe('linux')
    expect(defaultComputerId([computers[2]!])).toBe('laptop')
    expect(defaultComputerId([])).toBe('')
  })
})

describe('offlinePinnedComputer', () => {
  it('names a pinned computer that is offline or unknown, and nothing else', () => {
    const pin = (workerId: string) => ({ strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId, requiredCapabilities: [], allowedWorkerIds: [] })
    expect(offlinePinnedComputer(pin('laptop'), computers)).toBe('laptop')
    expect(offlinePinnedComputer(pin('gone'), computers)).toBe('gone')
    expect(offlinePinnedComputer(pin('mac'), computers)).toBeUndefined()
    expect(offlinePinnedComputer(undefined, computers)).toBeUndefined()
  })
})

describe('buildLaunchOverrides', () => {
  it('sends only what the user changed, and never a placement for the Lead', () => {
    const overrides = buildLaunchOverrides([
      { roleKey: 'lead', workerId: 'linux', model: 'opus', effort: '' },
      { roleKey: 'builder', workerId: '', model: '', effort: '' },
      { roleKey: 'tester', workerId: 'mac', model: '', effort: 'low' },
    ], 'lead')
    expect(overrides).toEqual([
      { roleKey: 'lead', options: { model: 'opus' } },
      { roleKey: 'tester', placement: { strategy: WorkerPlacementStrategy.SPECIFIC_WORKER, workerId: 'mac' }, options: { effort: 'low' } },
    ])
  })
})

describe('buildRoleUpdate', () => {
  it('always names the role, even with nothing changed', () => {
    expect(buildRoleUpdate({ roleKey: 'builder', workerId: '', model: '', effort: '' }, false)).toEqual({ roleKey: 'builder' })
    expect(buildRoleUpdate({ roleKey: 'lead', workerId: 'mac', model: 'x', effort: '' }, true)).toEqual({ roleKey: 'lead', options: { model: 'x' } })
  })
})

describe('profileDraft', () => {
  it('round-trips a profile through profileInit', () => {
    const init = profileInit(profileDraft(builder))
    expect(init).toMatchObject({
      id: 'p-builder',
      name: 'Builder',
      agentProvider: AgentProvider.CLAUDE_CODE,
      options: { model: 'opus', effort: 'high', permissionMode: 'acceptEdits' },
      placement: { strategy: WorkerPlacementStrategy.ANY_WORKER },
      maxInstances: 1,
    })
  })
})

describe('templateDraft', () => {
  it('round-trips a template through templateInit', () => {
    const init = templateInit(templateDraft(template))
    expect(init.roles?.map(r => r.key)).toEqual(['lead', 'builder', 'tester'])
    expect(init.roles?.[1]?.agent?.options).toEqual({ model: 'sonnet' })
    // A role with no placement override sends none, so it inherits its profile's.
    expect(init.roles?.[1]?.agent?.placement).toBeUndefined()
    expect(init.steps?.[1]).toMatchObject({ key: 'test', dependsOn: ['build'], approval: WorkflowApprovalGate.BEFORE_COMPLETE })
  })

  it('splits sensitive operations one per line', () => {
    const draft = { ...templateDraft(template), sensitiveOperations: 'git push\n\n  deploy  \n' }
    expect(templateInit(draft).sensitiveOperations).toEqual(['git push', 'deploy'])
  })

  it('leaves agents unconfined unless the template restricts them', () => {
    expect(templateDraft(template).confineAgents).toBe(false)
    expect(templateInit({ ...templateDraft(template), confineAgents: true }).confineAgents).toBe(true)
  })
})

describe('duplicateTemplateDraft', () => {
  it('clears the id and version and picks a free copy name', () => {
    const copy = duplicateTemplateDraft(template, ['Ship it', 'Ship it (copy)'])
    expect(copy.id).toBe('')
    expect(copy.version).toBe(0)
    expect(copy.name).toBe('Ship it (copy 2)')
  })
})

describe('setLeadRole', () => {
  it('leaves exactly one Lead', () => {
    const roles = setLeadRole(templateDraft(template).roles, 2)
    expect(roles.map(r => r.isLead)).toEqual([false, false, true])
  })
})

describe('renameStepKey', () => {
  it('renames dependencies with the step', () => {
    const steps = renameStepKey(templateDraft(template).steps, 0, 'compile')
    expect(steps[0]?.key).toBe('compile')
    expect(steps[1]?.dependsOn).toEqual(['compile'])
  })
})

describe('removeStep', () => {
  it('drops the dependencies on the removed step', () => {
    const steps = removeStep(templateDraft(template).steps, 0)
    expect(steps.map(s => s.key)).toEqual(['test'])
    expect(steps[0]?.dependsOn).toEqual([])
  })
})

describe('exportFileName', () => {
  it('prefers the worker\'s name and falls back to a slug', () => {
    expect(exportFileName('ship.yaml', 'x', false)).toBe('ship.yaml')
    expect(exportFileName('', 'Ship It!', true)).toBe('ship-it.json')
  })
})

describe('workflowErrorMessage', () => {
  it('drops the engine\'s category prefix', () => {
    expect(workflowErrorMessage(new Error('invalid workflow definition: template "x" needs exactly one Lead role, has 2')))
      .toBe('Template "x" needs exactly one Lead role, has 2')
    expect(workflowErrorMessage(new Error('not allowed in the current state: only a running or waiting run can be paused')))
      .toBe('Only a running or waiting run can be paused')
    expect(workflowErrorMessage(new Error('not found'))).toBe('It no longer exists on that computer.')
    expect(workflowErrorMessage(undefined, 'Fallback')).toBe('Fallback')
  })
})
