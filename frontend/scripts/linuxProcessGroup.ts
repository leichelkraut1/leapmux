import { readdirSync, readFileSync, statSync } from 'node:fs'
import process from 'node:process'

export interface LinuxProcessIdentity {
  pid: number
  pgid: number
  uid: number
  state: string
  startTicks: string
}

type GroupSnapshot = (groupId: number) => LinuxProcessIdentity[] | null

/** /proc comm can contain spaces and parentheses; numeric fields follow its final closing parenthesis. */
export function parseLinuxProcessStat(pid: number, uid: number, raw: string): LinuxProcessIdentity | null {
  const prefix = /^([1-9]\d*)\s+\(/.exec(raw)
  const end = raw.lastIndexOf(')')
  if (!prefix || Number(prefix[1]) !== pid || end < prefix[0].length || raw.length > 8192)
    return null
  const fields = raw.slice(end + 1).trim().split(/\s+/)
  const state = fields[0]
  const group = fields[2]
  const startTicks = fields[19]
  if (!state || !/^[A-Z]$/i.test(state) || !group || !/^\d+$/.test(group) || !startTicks || !/^\d+$/.test(startTicks)
    || !Number.isSafeInteger(pid) || pid <= 0 || !Number.isSafeInteger(uid) || uid < 0) {
    return null
  }
  const pgid = Number(group)
  if (!Number.isSafeInteger(pgid) || pgid < 0)
    return null
  return { pid, pgid, uid, state, startTicks }
}

function readGroupSnapshot(groupId: number): LinuxProcessIdentity[] | null {
  try {
    const names = readdirSync('/proc').filter(name => /^\d+$/.test(name))
    if (names.length > 65536)
      return null
    const members: LinuxProcessIdentity[] = []
    for (const name of names) {
      const directory = `/proc/${name}`
      const before = statSync(directory)
      const row = parseLinuxProcessStat(Number(name), before.uid, readFileSync(`${directory}/stat`, 'utf8'))
      const after = statSync(directory)
      if (!row || before.uid !== after.uid || before.ino !== after.ino)
        return null
      if (row.pgid === groupId)
        members.push(row)
    }
    return members
  }
  catch {
    // Disappearing entries make the enumeration unstable too: do not skip a
    // possible live member whose fork/reap raced this observation.
    return null
  }
}

/** Positive evidence only. A failed/unstable census never overrides a live group signal probe. */
export function ownedLinuxGroupHasOnlyDeadMembers(
  groupId: number,
  snapshot: GroupSnapshot = readGroupSnapshot,
  uid = process.getuid?.(),
): boolean {
  if (!Number.isSafeInteger(groupId) || groupId <= 1 || uid === undefined || !Number.isSafeInteger(uid) || uid < 0)
    return false
  try {
    const first = snapshot(groupId)
    const dead = (rows: LinuxProcessIdentity[] | null): rows is LinuxProcessIdentity[] => !!rows?.length
      && rows.every(row => row.pgid === groupId && row.uid === uid && (row.state === 'Z' || row.state === 'X'))
    if (!dead(first))
      return false
    const second = snapshot(groupId)
    if (!dead(second))
      return false
    const identities = (rows: LinuxProcessIdentity[]) => [...rows].sort((left, right) => left.pid - right.pid).map(row => [row.pid, row.startTicks, row.pgid, row.uid, row.state])
    return JSON.stringify(identities(first)) === JSON.stringify(identities(second))
  }
  catch {
    return false
  }
}
