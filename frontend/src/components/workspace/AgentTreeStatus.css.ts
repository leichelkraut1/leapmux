import { style, styleVariants } from '@vanilla-extract/css'

export const badge = style({
  flexShrink: 0,
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  marginInlineStart: 'var(--space-1)',
  whiteSpace: 'nowrap',
})

export const color = styleVariants({
  working: { color: 'var(--primary)' },
  waiting: { color: 'var(--warning)' },
  idle: { color: 'var(--muted-foreground)' },
  offline: { color: 'var(--muted-foreground)' },
  unknown: { color: 'var(--muted-foreground)' },
})
