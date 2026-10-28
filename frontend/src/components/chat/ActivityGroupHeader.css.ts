import { style } from '@vanilla-extract/css'

export const header = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  width: '100%',
  padding: 'var(--space-2) 0',
  border: 0,
  background: 'transparent',
  color: 'var(--muted-foreground)',
  textAlign: 'left',
  cursor: 'pointer',
  font: 'inherit',
  selectors: {
    '&:hover': { color: 'var(--foreground)' },
    '&:focus-visible': { outline: '2px solid var(--primary)', outlineOffset: 2 },
  },
})

export const chevron = style({ flexShrink: 0 })
export const chevronExpanded = style([chevron, { transform: 'rotate(90deg)' }])
export const label = style({ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' })
export const count = style({ fontSize: '0.85em', opacity: 0.7 })
