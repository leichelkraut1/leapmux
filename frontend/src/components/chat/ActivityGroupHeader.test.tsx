import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { ActivityGroupHeader } from './ActivityGroupHeader'

describe('ActivityGroupHeader', () => {
  it('exposes expansion and live activity without remounting its control', () => {
    const [expanded, setExpanded] = createSignal(false)
    const [working, setWorking] = createSignal(true)
    render(() => <ActivityGroupHeader expanded={expanded()} working={working()} count={3} onToggle={() => setExpanded(value => !value)} />)
    const button = screen.getByRole('button', { name: 'Expand activity (3 messages)' })
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(button).toHaveTextContent('Working')
    button.focus()
    fireEvent.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    setWorking(false)
    expect(button).toHaveTextContent('Activity')
    expect(screen.getByRole('button', { name: 'Collapse activity (3 messages)' })).toBe(button)
    expect(button).toHaveFocus()
  })
})
