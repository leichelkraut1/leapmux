import type { MockModelRequestRecord } from './mockModelScript'
import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { runInNewContext } from 'node:vm'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { expectWorkerFileReference, workerFileReadCommand } from './workerFileAttachment'

let directory: string
let storage: string
let source: string
let uploaded: string

function reference(path: string): string {
  return `File available on this Worker at ${JSON.stringify(path)}.\nThe attachment is not inline model input.`
}

function request(text: string, earlier: unknown[] = []): MockModelRequestRecord {
  return {
    protocol: 'anthropic-messages',
    path: '/v1/messages',
    stepIndex: 0,
    body: {
      messages: [...earlier, { role: 'user', content: [{ type: 'text', text }] }],
    },
  }
}

beforeEach(() => {
  directory = mkdtempSync(join(tmpdir(), 'leapmux-worker-file-proof-'))
  storage = join(directory, 'worker')
  const uploads = join(storage, 'attachments', 'agent')
  mkdirSync(uploads, { recursive: true, mode: 0o700 })
  source = join(directory, 'source.ACD')
  uploaded = join(uploads, 'uploaded.ACD')
  writeFileSync(source, new Uint8Array([0, 255, 1, 254]))
  writeFileSync(uploaded, readFileSync(source))
})

afterEach(() => {
  if (!directory.startsWith(join(tmpdir(), 'leapmux-worker-file-proof-')))
    throw new Error('Unexpected test cleanup directory')
  rmSync(directory, { recursive: true, force: true })
})

describe('workerFileReadCommand', () => {
  it.each([
    'C:\\Users\\Test User\\files\\a $(ignored) `value` "quote".ACD',
    '/tmp/files/a $(ignored) `value` "quote" and \'single\'.ACD',
  ])('keeps %s inside a base64 payload and reads its exact bytes', (path) => {
    const command = workerFileReadCommand(path)
    const match = /^node -e "eval\(Buffer\.from\('([A-Za-z0-9+/=]+)','base64'\)\.toString\(\)\)"$/.exec(command)
    expect(match).not.toBeNull()
    const script = Buffer.from(match![1]!, 'base64').toString()
    const bytes = Buffer.from([0, 255, 1, 254])
    const readFile = vi.fn().mockReturnValue(bytes)
    const write = vi.fn()
    const requireModule = vi.fn((name: string) => {
      expect(name).toBe('node:fs')
      return { readFileSync: readFile }
    })
    runInNewContext(script, { require: requireModule, process: { stdout: { write } } })
    expect(readFile).toHaveBeenCalledExactlyOnceWith(path)
    expect(write).toHaveBeenCalledExactlyOnceWith(`WORKER_FILE_BYTES:${bytes.toString('base64')}`)
    expect(command).not.toContain('$(ignored)')
    expect(command).not.toContain('`value`')
  })
})

describe('expectWorkerFileReference', () => {
  it('proves current-user file references using exact binary bytes', () => {
    expect(expectWorkerFileReference(request(reference(uploaded)), source, storage)).toBe(realpathSync(uploaded))
  })

  it('refuses an earlier user reference without one in the current turn', () => {
    const earlier = [
      { role: 'user', content: reference(uploaded) },
      { role: 'assistant', content: 'Earlier attachment.' },
    ]
    expect(() => expectWorkerFileReference(request('No attachment.', earlier), source, storage)).toThrow('exactly one Worker file reference')
  })

  it('refuses a reference only in system instructions', () => {
    expect(() => expectWorkerFileReference(request('No attachment.', [{ role: 'system', content: reference(uploaded) }]), source, storage)).toThrow('exactly one Worker file reference')
  })

  it('refuses a file outside private attachment storage', () => {
    expect(() => expectWorkerFileReference(request(reference(source)), source, storage)).toThrow('escaped private Worker attachment storage')
  })

  it('refuses a path whose bytes differ from the uploaded file', () => {
    writeFileSync(uploaded, 'unrelated bytes')
    expect(() => expectWorkerFileReference(request(reference(uploaded)), source, storage)).toThrow('preserves every uploaded byte')
  })
})
