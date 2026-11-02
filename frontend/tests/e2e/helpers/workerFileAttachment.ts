import type { MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { readFileSync, realpathSync } from 'node:fs'
import { isAbsolute, relative, sep } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentUserTextParts, exerciseAttachmentDelivery } from './attachmentModelProbe'
import { nativeToolOutcome } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { bashToolCall, codexCompletedEscalatedCommandToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { userBubbles, waitForNativeSettingsHydrated } from './ui'

const LOCAL_FILE_REFERENCE = /^File available on this Worker at ("(?:[^"\\]|\\.)*")\.$/gm

/** Prove a current-user path reference and its exact bytes inside this test's private Worker data. */
export function expectWorkerFileReference(request: MockModelRequestRecord, sourcePath: string, storageRoot: string): string {
  const content = currentUserTextParts(request).join('\n')
  const references = [...content.matchAll(LOCAL_FILE_REFERENCE)]
  expect(references, 'the current user input carries exactly one Worker file reference').toHaveLength(1)
  const quotedPath = references[0]?.[1]
  if (quotedPath === undefined)
    throw new Error('the attachment has no quoted Worker file path')
  const path: unknown = JSON.parse(quotedPath)
  if (typeof path !== 'string' || !isAbsolute(path))
    throw new Error('the attachment reference must be an absolute Worker path')
  const resolvedRoot = realpathSync(storageRoot)
  const resolvedFile = realpathSync(path)
  const within = relative(resolvedRoot, resolvedFile)
  if (within === '' || within === '..' || within.startsWith(`..${sep}`) || isAbsolute(within)
    || !within.split(sep).includes('attachments')) {
    throw new Error('the attachment reference escaped private Worker attachment storage')
  }
  expect(readFileSync(resolvedFile), 'the referenced Worker file preserves every uploaded byte').toEqual(readFileSync(sourcePath))
  expect(content).toContain('The attachment is not inline model input.')
  return resolvedFile
}

/** A synthetic fixture read with no path or source text exposed to either shell's parser. */
export function workerFileReadCommand(workerPath: string): string {
  const script = `process.stdout.write('WORKER_FILE_' + 'BYTES:' + require('node:fs').readFileSync(${JSON.stringify(workerPath)}).toString('base64'))`
  const encoded = Buffer.from(script).toString('base64')
  return `node -e "eval(Buffer.from('${encoded}','base64').toString())"`
}

/** Attach an arbitrary file, prove delivery, then read its bytes using the agent's real tool after a browser reload. */
export async function exerciseWorkerFileAttachment(
  context: NativeScenarioContext,
  options: { kind: 'binary' | 'pdf', fileName: string },
): Promise<MockModelRequestRecord> {
  let workerPath = ''
  let sourcePath = ''
  const request = await exerciseAttachmentDelivery(context, options.kind, options.fileName, {
    proof: (record, source) => {
      sourcePath = source
      workerPath = expectWorkerFileReference(record, source, getGlobalState().dataDir)
    },
  })
  await context.page.reload()
  await waitForNativeSettingsHydrated(context.page)
  await expect(userBubbles(context.page).filter({ hasText: options.fileName }).last()).toBeVisible()
  const callId = 'read-worker-file-bytes'
  const command = workerFileReadCommand(workerPath)
  const read = await runNativeToolTurn(context, {
    prompt: 'Inspect the previously attached Worker file using available tools.',
    answer: 'The saved attachment remains available to tools.',
    // Exercise the normal approval flow for this precise outside-workspace read.
    // The Codex sandbox is unavailable inside a default Docker namespace.
    toolCalls: [context.provider === AgentProvider.CODEX
      ? codexCompletedEscalatedCommandToolCall(callId, command)
      : bashToolCall(context.provider, callId, command)],
  })
  const result = await nativeToolOutcome(context, read.resultRequest, callId)
  expect(result.failed).not.toBe(true)
  if (result.exitCode !== undefined)
    expect(result.exitCode).toBe(0)
  expect(result.text).toContain(`WORKER_FILE_BYTES:${readFileSync(sourcePath).toString('base64')}`)
  expect(readFileSync(workerPath)).toEqual(readFileSync(sourcePath))
  return request
}
