import type { TestInfo } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from './nativeMessages'
import { currentNativeAgent, nativeToolOutcome } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { bashToolCall, codexCompletedEscalatedCommandToolCall } from './providerToolCalls'
import { activityGroupToggles, assistantBubbles, expandActivityGroups, toolRows } from './ui'

/** Exercise default folding through native messages, actual approvals and actual tool output. */
export async function exerciseActivityGroups(context: ManagedNativeScenarioContext, testInfo: Pick<TestInfo, 'attach'>): Promise<void> {
  const callId = 'folded-native-command'
  const marker = 'LEAPMUX_FOLDED_RESULT'
  const command = `node -e "process.stdout.write('LEAPMUX_' + 'FOLDED_RESULT\\n')"`
  const answer = 'Activity finished. The final reply stays visible.'
  const turn = await runNativeToolTurn(context, {
    prompt: 'Run the small activity disclosure check.',
    answer,
    reasoning: 'Check the command output before answering.',
    toolCalls: [context.provider === AgentProvider.CODEX
      ? codexCompletedEscalatedCommandToolCall(callId, command)
      : bashToolCall(context.provider, callId, command)],
  })
  const outcome = await nativeToolOutcome(context, turn.resultRequest, callId)
  expect(outcome.failed).not.toBe(true)
  if (outcome.exitCode !== undefined)
    expect(outcome.exitCode).toBe(0)
  expect(outcome.text).toContain(marker)
  // Keep the real Worker frames before fixture teardown removes its private data.
  const agent = await currentNativeAgent(context)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  await testInfo.attach('native-activity-messages', {
    body: JSON.stringify({
      agentId: snapshot.agentId,
      agentSessionId: snapshot.agentSessionId,
      messages: snapshot.messages.map(message => ({
        id: message.id,
        seq: message.seq.toString(),
        source: message.source,
        agentProvider: message.agentProvider,
        agentSessionId: message.agentSessionId,
        spanId: message.spanId,
        spanType: message.spanType,
        completion: message.completion,
        contentCompression: message.contentCompression,
        contentBase64: Buffer.from(message.content).toString('base64'),
        frame: nativeMessageBody(message),
        supplementalContentCompression: message.supplementalContentCompression,
        supplementalContentBase64: Buffer.from(message.supplementalContent).toString('base64'),
        supplement: nativeMessageSupplement(message),
      })),
    }, null, 2),
    contentType: 'application/json',
  })
  const groups = activityGroupToggles(context.page)
  await expect(groups.first()).toBeVisible()
  expect(await groups.evaluateAll(elements => elements.every(element => element.getAttribute('aria-expanded') === 'false'))).toBe(true)
  await expect(toolRows(context.page)).toHaveCount(0)
  await expect(assistantBubbles(context.page).filter({ hasText: answer })).toBeVisible()
  await expandActivityGroups(context.page)
  await expect(toolRows(context.page).filter({ hasText: marker }).first()).toBeVisible()
  await expect(groups.first()).toHaveAttribute('aria-expanded', 'true')
  await groups.first().click()
  await expect(groups.first()).toHaveAttribute('aria-expanded', 'false')
  await expect(assistantBubbles(context.page).filter({ hasText: answer })).toBeVisible()
}
