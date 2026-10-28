/// <reference types="vitest/globals" />
import { createComponent, createRoot, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { HubControlEvent } from '~/generated/proto/leapmux/v1/channel_pb'
import { createDialogState } from '~/hooks/createDialogState'
import { deferred, flush as flushMicrotasks, untilTrue } from '~/test-support/async'
import { useWorkerSection } from './useWorkerSection'

const mockListWorkers = vi.fn()
const mockDeregisterWorker = vi.fn()
vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: (...a: unknown[]) => mockListWorkers(...a),
    deregisterWorker: (...a: unknown[]) => mockDeregisterWorker(...a),
  },
}))

const hubControlHandlers: Array<(frame: { events: HubControlEvent[] }) => void> = []

/**
 * The hub-control handler the connect registered; the throw keeps a missing
 * registration failing loudly and doubles as the type-level guard.
 */
function invokeHubControl(frame: { events: HubControlEvent[] }): void {
  const handler = hubControlHandlers[0]
  if (handler === undefined)
    throw new Error('expected a hub control handler')
  handler(frame)
}
const mockSetConfirmKeyPin = vi.fn()
const mockUnregisterKeyPin = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    // Returns an unsubscribe, like the real `ChannelManager.onHubControl`.
    // A double that returned nothing here would make `onCleanup(...)` throw on
    // dispose -- and, worse, would let a regression that DROPS the unsubscribe
    // pass silently, which is the leak this hook had.
    onHubControl: (fn: (frame: { events: HubControlEvent[] }) => void) => {
      hubControlHandlers.push(fn)
      return () => {
        const i = hubControlHandlers.indexOf(fn)
        if (i >= 0)
          hubControlHandlers.splice(i, 1)
      }
    },
  },
  // Returns a disposer, like the real `setConfirmKeyPin`. A double that
  // returned nothing would make `onCleanup(...)` throw on dispose AND would let
  // a regression that drops the unregister pass silently -- the same divergence
  // that hid this hook's leak until the `onHubControl` double was fixed.
  setConfirmKeyPin: (fn: unknown) => {
    mockSetConfirmKeyPin(fn)
    return mockUnregisterKeyPin
  },
}))

const mockFetchWorkerInfo = vi.fn()
vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    workerInfo: vi.fn(),
    fetchWorkerInfo: (...a: unknown[]) => mockFetchWorkerInfo(...a),
  },
}))

vi.mock('~/stores/workerChannelStatus.store', () => ({
  createWorkerChannelStatusStore: () => ({ getStatus: vi.fn() }),
}))
vi.mock('~/stores/tunnel.store', () => ({ createTunnelStore: () => ({ tunnels: vi.fn() }) }))

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function worker(id: string, online = true) {
  return { id, online, name: id }
}

beforeEach(() => {
  hubControlHandlers.length = 0
  mockListWorkers.mockReset()
  mockListWorkers.mockResolvedValue({ workers: [worker('w1')] })
  mockDeregisterWorker.mockReset()
  mockDeregisterWorker.mockResolvedValue({})
  mockFetchWorkerInfo.mockReset()
  mockSetConfirmKeyPin.mockReset()
  mockUnregisterKeyPin.mockReset()
})

/**
 * The worker registry, lifted out of `AppShell`. It shares nothing with the
 * rest of that component — no tabs, no layout, no projection — so it is
 * testable on its own, which it was not while it lived inline.
 */
describe('useWorkerSection', () => {
  function mount(getUserId: () => string = () => 'u1') {
    return useWorkerSection({ getUserId, keyPinConfirmDialog: createDialogState() })
  }

  it('fetches workers once the user id is known', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(1)
      expect(s.workers().map(w => w.id)).toEqual(['w1'])
      dispose()
    })
  })

  it('does not fetch before the session restores a user', async () => {
    await createRoot(async (dispose) => {
      mount(() => '')
      await flush()
      // A blank user id means the session has not restored yet; listing then
      // would be an unauthenticated round-trip on every cold load.
      expect(mockListWorkers).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('keeps worker object identity stable across refetches', async () => {
    await createRoot(async (dispose) => {
      // listWorkers deserializes FRESH objects every call, so the mock must
      // too -- `mockResolvedValue` hands back one shared object, which would
      // preserve identity on its own and make this assertion vacuous.
      mockListWorkers.mockImplementation(async () => ({ workers: [worker('w1')] }))
      const s = mount()
      await flush()
      const first = s.workers()[0]

      invokeHubControl({ events: [HubControlEvent.WORKERS_CHANGED] })
      await flush()

      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      expect(s.workers()[0], 'same reference, so the sidebar row is not re-created').toBe(first)
      dispose()
    })
  })

  it('ignores hub control frames that are not WORKERS_CHANGED', async () => {
    await createRoot(async (dispose) => {
      mount()
      await flush()
      expect(mockListWorkers).toHaveBeenCalledTimes(1)

      invokeHubControl({ events: [] })
      await flush()

      expect(mockListWorkers).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('warms worker info only for ONLINE workers', async () => {
    await createRoot(async (dispose) => {
      mockListWorkers.mockResolvedValue({ workers: [worker('up', true), worker('down', false)] })
      mount()
      await flush()

      expect(mockFetchWorkerInfo).toHaveBeenCalledTimes(1)
      expect(mockFetchWorkerInfo).toHaveBeenCalledWith('up')
      dispose()
    })
  })

  // The count alone proves nothing -- the hook calls `setConfirmKeyPin` once,
  // unconditionally, in its body, so `toHaveBeenCalledTimes(1)` after a single
  // mount holds for every implementation that registers at all. What has to be
  // pinned is the DISPOSE half, and for the same reason as the hub-control
  // subscription below: the registration outlives the mount otherwise, and a
  // prompt closed over a dead mount's dialog never settles -- `KeyPinStore`
  // awaits it with no timeout and queues every later prompt behind it, so one
  // TOFU mismatch in that window deadlocks key-pinning for the whole page.
  it('registers the key-pin prompt for the live mount and drops it on dispose', async () => {
    await createRoot(async (dispose) => {
      mount()
      await flush()
      expect(mockSetConfirmKeyPin).toHaveBeenCalledTimes(1)
      expect(mockUnregisterKeyPin, 'still mounted, so still registered').not.toHaveBeenCalled()

      dispose()

      expect(mockUnregisterKeyPin, 'the registration must not outlive the mount').toHaveBeenCalledTimes(1)
    })
  })

  it('unsubscribes from hub control frames when the owner disposes', async () => {
    await createRoot(async (dispose) => {
      mount()
      await flush()
      expect(hubControlHandlers).toHaveLength(1)

      dispose()

      // `channelManager` is a module-level singleton that outlives this owner.
      // AppShell really does remount inside one page lifetime (logout navigates
      // to /login client-side, logging back in navigates to /), so a retained
      // listener would fan out one `listWorkers` plus a `fetchWorkerInfo` per
      // online worker for every stale mount, on every WORKERS_CHANGED frame.
      expect(hubControlHandlers, 'the listener must not outlive the mount').toHaveLength(0)
    })
  })

  it('survives a failed listWorkers with an empty list', async () => {
    await createRoot(async (dispose) => {
      mockListWorkers.mockRejectedValue(new Error('offline'))
      const s = mount()
      await flush()
      expect(s.workers()).toEqual([])
      dispose()
    })
  })

  /**
   * Deregistration removes the worker from the list, and NOTHING else.
   *
   * It used to clear the worker's keyed git state too. That left every tab of
   * the worker under its repo with no branch name, for the life of the page:
   * nothing removes those tab rows, they keep `gitToplevel`, the sidebar groups
   * a tab by that field, and the branch label comes from the store. It is the
   * same defect the worker-offline sweep used to cause, from the other trigger.
   */
  it('removes the worker from the list and leaves its keyed git state alone', async () => {
    await createRoot(async (dispose) => {
      const s = mount(() => 'u1')
      await flush()

      s.openWorkerSettings(worker('w1') as never)
      const host = document.createElement('div')
      document.body.appendChild(host)
      const unmount = render(() => createComponent(s.Dialogs, {}), host)
      await flush()

      host.querySelector<HTMLButtonElement>('[data-testid="deregister-confirm"]')!.click()
      await flush()

      expect(mockDeregisterWorker).toHaveBeenCalledWith({ workerId: 'w1' })
      expect(s.workers().map(w => w.id)).toEqual([])

      unmount()
      host.remove()
      dispose()
    })
  })

  it('distinguishes a pending computer list from a successful empty list', async () => {
    const pending = deferred<{ workers: ReturnType<typeof worker>[] }>()
    mockListWorkers.mockReturnValue(pending.promise)
    await createRoot(async (dispose) => {
      const s = mount()
      await flushMicrotasks()
      expect(s.registryStatus()).toBe('loading')
      expect(s.workers()).toEqual([])
      pending.resolve({ workers: [] })
      await untilTrue(() => s.registryStatus() === 'available')
      expect(s.workers()).toEqual([])
      dispose()
    })
  })

  it('loads every cursor page before publishing a complete computer list', async () => {
    mockListWorkers
      .mockResolvedValueOnce({ workers: [worker('first')], page: { hasMore: true, nextCursor: 'page-two' } })
      .mockResolvedValueOnce({ workers: [worker('last')], page: { hasMore: false, nextCursor: '' } })
    await createRoot(async (dispose) => {
      const s = mount()
      await untilTrue(() => s.registryStatus() === 'available')
      expect(s.workers().map(w => w.id)).toEqual(['first', 'last'])
      expect(mockListWorkers).toHaveBeenNthCalledWith(1, { page: { cursor: '' } }, expect.objectContaining({ signal: expect.any(AbortSignal) }))
      expect(mockListWorkers).toHaveBeenNthCalledWith(2, { page: { cursor: 'page-two' } }, expect.objectContaining({ signal: expect.any(AbortSignal) }))
      dispose()
    })
  })

  it('retains an available registry during manual and pushed refreshes without a loading transition', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await untilTrue(() => s.registryStatus() === 'available')
      const original = s.workers()[0]
      const manual = deferred<{ workers: ReturnType<typeof worker>[] }>()
      mockListWorkers.mockReturnValueOnce(manual.promise)
      const refresh = s.refreshWorkers()
      expect(s.registryStatus()).toBe('available')
      expect(s.workers()[0]).toBe(original)
      manual.resolve({ workers: [worker('w1')] })
      await refresh
      expect(s.workers()[0]).toBe(original)

      const pushed = deferred<{ workers: ReturnType<typeof worker>[] }>()
      mockListWorkers.mockReturnValueOnce(pushed.promise)
      invokeHubControl({ events: [HubControlEvent.WORKERS_CHANGED] })
      expect(s.registryStatus()).toBe('available')
      expect(s.workers()[0]).toBe(original)
      pushed.reject(new Error('connection unavailable'))
      await untilTrue(() => s.registryStatus() === 'unavailable')
      expect(s.workers()[0]).toBe(original)
      expect(mockListWorkers).toHaveBeenCalledTimes(3)
      dispose()
    })
  })

  it('clears the retained registry when the authenticated account changes', async () => {
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('u1')
      const s = mount(userId)
      await untilTrue(() => s.registryStatus() === 'available')
      const nextUser = deferred<{ workers: ReturnType<typeof worker>[] }>()
      mockListWorkers.mockReturnValueOnce(nextUser.promise)
      setUserId('u2')
      expect(s.registryStatus()).toBe('loading')
      expect(s.workers()).toEqual([])
      nextUser.resolve({ workers: [worker('other-account')] })
      await untilTrue(() => s.registryStatus() === 'available')
      expect(s.workers().map(w => w.id)).toEqual(['other-account'])
      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      dispose()
    })
  })

  it('keeps the previous sidebar list when a later page fails and recovers through refresh', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      await untilTrue(() => s.registryStatus() === 'available')
      mockListWorkers
        .mockResolvedValueOnce({ workers: [worker('partial')], page: { hasMore: true, nextCursor: 'next' } })
        .mockRejectedValueOnce(new Error('page unavailable'))
      await s.refreshWorkers()
      expect(s.registryStatus()).toBe('unavailable')
      expect(s.workers().map(w => w.id)).toEqual(['w1'])
      expect(mockFetchWorkerInfo).not.toHaveBeenCalledWith('partial')
      mockListWorkers.mockResolvedValueOnce({ workers: [worker('recovered')] })
      await s.refreshWorkers()
      expect(s.registryStatus()).toBe('available')
      expect(s.workers().map(w => w.id)).toEqual(['recovered'])
      dispose()
    })
  })

  it('stops a repeated pagination cursor without publishing a partial list', async () => {
    mockListWorkers.mockResolvedValue({
      workers: [worker('partial')],
      page: { hasMore: true, nextCursor: 'repeated' },
    })
    await createRoot(async (dispose) => {
      const s = mount()
      await untilTrue(() => s.registryStatus() === 'unavailable')
      expect(mockListWorkers).toHaveBeenCalledTimes(2)
      expect(s.workers()).toEqual([])
      dispose()
    })
  })

  it('ignores a superseded response even when the transport ignores abort', async () => {
    const pending = deferred<{ workers: ReturnType<typeof worker>[] }>()
    mockListWorkers.mockReturnValueOnce(pending.promise).mockResolvedValueOnce({ workers: [worker('current')] })
    await createRoot(async (dispose) => {
      const s = mount()
      await flushMicrotasks()
      const signal = mockListWorkers.mock.calls[0]![1].signal as AbortSignal
      await s.refreshWorkers()
      expect(signal.aborted).toBe(true)
      pending.resolve({ workers: [worker('stale')] })
      await flushMicrotasks()
      expect(s.registryStatus()).toBe('available')
      expect(s.workers().map(w => w.id)).toEqual(['current'])
      dispose()
    })
  })

  it('aborts the pending registry request and ignores its reply after disposal', async () => {
    const pending = deferred<{ workers: ReturnType<typeof worker>[] }>()
    mockListWorkers.mockReturnValue(pending.promise)
    await createRoot(async (dispose) => {
      const s = mount()
      await flushMicrotasks()
      const signal = mockListWorkers.mock.calls[0]![1].signal as AbortSignal
      dispose()
      expect(signal.aborted).toBe(true)
      pending.resolve({ workers: [worker('late')] })
      await flushMicrotasks()
      expect(s.workers()).toEqual([])
      expect(mockFetchWorkerInfo).not.toHaveBeenCalled()
    })
  })
})
