import type { LinuxProcessIdentity } from './linuxProcessGroup'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { readFileSync, statSync } from 'node:fs'
import process from 'node:process'
import { createInterface } from 'node:readline'
import { describe, expect, it, vi } from 'vitest'
import { ownedLinuxGroupHasOnlyDeadMembers, parseLinuxProcessStat } from './linuxProcessGroup'

function stat(pid = 123, comm = 'generated (name) with ) spaces', state = 'Z', group = 123, start = '90071992547409931234') {
  const fields = Array.from({ length: 20 }).fill('0')
  fields[0] = state
  fields[1] = '1'
  fields[2] = String(group)
  fields[19] = start
  return `${pid} (${comm}) ${fields.join(' ')}\n`
}

function member(extra: Partial<LinuxProcessIdentity> = {}): LinuxProcessIdentity {
  return { pid: 124, pgid: 123, uid: 10002, state: 'Z', startTicks: '12345678901234567890', ...extra }
}

describe('parseLinuxProcessStat', () => {
  it('handles spaces and nested parentheses without rounding the creation identity', () => {
    expect(parseLinuxProcessStat(123, 10002, stat())).toEqual({ pid: 123, uid: 10002, pgid: 123, state: 'Z', startTicks: '90071992547409931234' })
  })

  it.each(['', 'not a process', '123 (truncated) Z 1 123', stat(999), stat(123, 'name', 'ZZ'), stat(123, 'name', 'Z', 123, 'missing')])('rejects ambiguous process metadata %s', (raw) => {
    expect(parseLinuxProcessStat(123, 10002, raw)).toBeNull()
  })
})

describe('ownedLinuxGroupHasOnlyDeadMembers', () => {
  it('requires two matching all-dead owned snapshots, independent of enumeration order', () => {
    const snapshot = vi.fn().mockReturnValueOnce([member(), member({ pid: 125, state: 'X' })]).mockReturnValueOnce([member({ pid: 125, state: 'X' }), member()])
    expect(ownedLinuxGroupHasOnlyDeadMembers(123, snapshot, 10002)).toBe(true)
    expect(snapshot).toHaveBeenCalledTimes(2)
  })

  it.each(['R', 'S', 'D', 'T', 'I', '?'])('keeps an existing group live or ambiguous for member state %s', (state) => {
    expect(ownedLinuxGroupHasOnlyDeadMembers(123, () => [member({ state })], 10002)).toBe(false)
  })

  it('refuses unreadable, empty, foreign-owned, and wrong-group observations', () => {
    for (const rows of [null, [], [member({ uid: 0 })], [member({ pgid: 999 })]])
      expect(ownedLinuxGroupHasOnlyDeadMembers(123, () => rows, 10002)).toBe(false)
    expect(ownedLinuxGroupHasOnlyDeadMembers(123, () => {
      throw new Error('generated denied census')
    }, 10002)).toBe(false)
  })

  it.each([
    [member(), member({ pid: 125 })],
    [member({ startTicks: 'new-identity' })],
    [member({ state: 'X' })],
    [member({ uid: 0 })],
    [member({ state: 'R' })],
  ])('rejects a changing or forked group membership snapshot %s', (...rows) => {
    const snapshot = vi.fn().mockReturnValueOnce([member()]).mockReturnValueOnce(rows)
    expect(ownedLinuxGroupHasOnlyDeadMembers(123, snapshot, 10002)).toBe(false)
  })

  it.runIf(process.platform === 'linux')('recognizes an actual unreaped owned zombie without abandoning its reaper', async () => {
    // Suspend only our own small parent so libuv cannot reap its exact child.
    // Resuming it in finally reaps the zombie; nothing is orphaned to PID 1.
    const source = `
      const { spawn } = require('node:child_process');
      const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore', env: {} });
      child.once('error', () => process.exit(1));
      child.once('spawn', () => process.stdout.write(JSON.stringify({ pid: child.pid }) + '\\n'));
      child.once('exit', () => process.exit(0));
      process.stdin.on('data', () => child.kill('SIGKILL'));
    `
    const parent = spawn(process.execPath, ['-e', source], { stdio: ['pipe', 'pipe', 'ignore'], env: {} })
    parent.stdin.on('error', () => {})
    const exited = once(parent, 'exit')
    const lines = createInterface({ input: parent.stdout })
    let stopped = false
    try {
      const [line] = await Promise.race([
        once(lines, 'line'),
        exited.then(() => { throw new Error('The generated reaper exited before its child identity.') }),
      ])
      const value: unknown = JSON.parse(String(line))
      if (!value || typeof value !== 'object' || !('pid' in value) || typeof value.pid !== 'number' || !Number.isSafeInteger(value.pid) || value.pid <= 1 || !parent.pid)
        throw new Error('The generated child identity is invalid.')
      const pid = value.pid
      const row = () => parseLinuxProcessStat(pid, statSync(`/proc/${pid}`).uid, readFileSync(`/proc/${pid}/stat`, 'utf8'))
      const before = row()
      expect(before?.pgid).toBe(pid)
      expect(before?.uid).toBe(process.getuid!())
      process.kill(parent.pid, 'SIGSTOP')
      stopped = true
      await expect.poll(() => {
        const raw = readFileSync(`/proc/${parent.pid}/stat`, 'utf8')
        return parseLinuxProcessStat(parent.pid!, process.getuid!(), raw)?.state
      }).toBe('T')
      process.kill(pid, 'SIGKILL')
      await expect.poll(() => row()?.state).toBe('Z')
      expect(row()?.startTicks).toBe(before?.startTicks)
      expect(() => process.kill(-pid, 0)).not.toThrow()
      await expect.poll(() => ownedLinuxGroupHasOnlyDeadMembers(pid)).toBe(true)
    }
    finally {
      lines.close()
      if (stopped && parent.pid && parent.exitCode === null)
        process.kill(parent.pid, 'SIGCONT')
      if (parent.exitCode === null)
        parent.stdin.end('cleanup\n')
      await exited
    }
  })
})
