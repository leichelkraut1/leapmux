import { create } from '@bufbuild/protobuf'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression } from '~/generated/proto/leapmux/v1/agent_pb'
import { PromptCacheNotice } from './PromptCacheNotice'
import '~/components/chat/providers'

const START = Date.parse('2026-11-01T12:00:00Z')
function messages(ttl: '5m' | '1h' | undefined = '5m') {
  return [create(AgentChatMessageSchema, {
    id: 'generated',
    seq: 1n,
    contentCompression: ContentCompression.NONE,
    createdAt: new Date(START).toISOString(),
    content: new TextEncoder().encode(JSON.stringify({
      type: 'assistant',
      message: { id: 'generated-native', model: 'generated-model', usage: {
        input_tokens: 100,
        cache_read_input_tokens: 49_899,
        cache_creation_input_tokens: 1,
        ...(ttl ? { cache_creation: { [ttl === '1h' ? 'ephemeral_1h_input_tokens' : 'ephemeral_5m_input_tokens']: 1 } } : {}),
      } },
    })),
  })]
}
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('PromptCacheNotice', () => {
  it('expires at the five-minute boundary using only the ui clock', () => {
    vi.useFakeTimers()
    vi.setSystemTime(START + 285_000)
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={messages()} />)
    expect(screen.getByRole('status').textContent).toContain('likely warm')
    vi.advanceTimersByTime(15_000)
    expect(screen.getByRole('status').textContent).toContain('likely expired')
    expect(screen.getByRole('status').textContent).toContain('about 50k input tokens')
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('keeps the one-hour lifetime warm beyond five minutes', () => {
    vi.useFakeTimers()
    vi.setSystemTime(START + 300_000)
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={messages('1h')} />)
    expect(screen.getByRole('status').textContent).toContain('55 min left')
    vi.setSystemTime(START + 3_585_000)
    vi.advanceTimersByTime(15_000)
    expect(screen.getByRole('status').textContent).toContain('likely expired')
  })

  it('labels missing ttl and future timestamps unavailable without an exact token claim', () => {
    vi.useFakeTimers()
    vi.setSystemTime(START - 1000)
    const first = render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={messages()} />)
    expect(screen.getByRole('status').textContent).toContain('unavailable')
    first.unmount()
    vi.setSystemTime(START + 600_000)
    const missing = messages()
    const payload = JSON.parse(new TextDecoder().decode(missing[0]!.content))
    delete payload.message.usage.cache_creation
    missing[0]!.content = new TextEncoder().encode(JSON.stringify(payload))
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={missing} />)
    expect(screen.getByRole('status').textContent).toContain('unavailable')
    expect(screen.getByRole('status').textContent).not.toContain('50k')
  })

  it('labels native estimates and refuses stale or non-tail histories', () => {
    vi.useFakeTimers()
    vi.setSystemTime(START + 600_000)
    const first = render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={messages()} savedHistory observedAt={new Date(Date.now()).toISOString()} />)
    expect(screen.getByRole('status').textContent).toContain('Saved-history estimate: Context idle')
    first.unmount()
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={messages()} savedHistory current={false} observedAt={new Date(START).toISOString()} />)
    expect(screen.getByRole('status').textContent).toContain('refresh the latest history')
    expect(screen.getByRole('status').textContent).not.toContain('50k')
  })

  it('accepts a fresh observation between clock ticks without accepting a future timestamp', () => {
    vi.useFakeTimers()
    vi.setSystemTime(START + 600_000)
    const [observedAt, setObservedAt] = createSignal(new Date(Date.now()).toISOString())
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={messages()} savedHistory observedAt={observedAt()} />)
    expect(screen.getByRole('status').textContent).toContain('likely expired')
    // Move the wall clock and poll metadata, without firing the 15-second UI timer.
    vi.setSystemTime(START + 602_000)
    setObservedAt(new Date(Date.now()).toISOString())
    expect(screen.getByRole('status').textContent).toContain('likely expired')
    expect(screen.getByRole('status').textContent).not.toContain('unavailable')
    setObservedAt(new Date(Date.now() + 2000).toISOString())
    expect(screen.getByRole('status').textContent).toContain('unavailable')
  })

  it('does not render a provider that supplies no cache evidence capability', () => {
    vi.useFakeTimers()
    render(() => <PromptCacheNotice provider={AgentProvider.CODEX} messages={messages()} />)
    expect(screen.queryByTestId('prompt-cache-notice')).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps fresh chats without usage quiet and starts no clock for them', () => {
    vi.useFakeTimers()
    const withoutUsage = create(AgentChatMessageSchema, {
      contentCompression: ContentCompression.NONE,
      content: new TextEncoder().encode(JSON.stringify({ type: 'assistant', message: { content: [{ type: 'text', text: 'Generated text with no usage' }] } })),
    })
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={[withoutUsage]} />)
    expect(screen.queryByTestId('prompt-cache-notice')).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('withdraws the old numeric estimate after compaction', () => {
    vi.useFakeTimers()
    vi.setSystemTime(START + 600_000)
    const boundary = create(AgentChatMessageSchema, {
      contentCompression: ContentCompression.NONE,
      id: 'generated-compaction',
      seq: 2n,
      content: new TextEncoder().encode(JSON.stringify({ type: 'system', subtype: 'compact_boundary' })),
    })
    render(() => <PromptCacheNotice provider={AgentProvider.CLAUDE_CODE} messages={[...messages(), boundary]} />)
    expect(screen.getByRole('status').textContent).toContain('estimate unavailable after compaction')
    expect(screen.getByRole('status').textContent).not.toContain('50k')
    expect(screen.getByRole('status').textContent).not.toContain('will re-cache')
  })
})
