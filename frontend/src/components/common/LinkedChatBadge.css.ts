import { style } from '@vanilla-extract/css'

export const badge = style({
  display: 'inline-flex',
  alignItems: 'center',
  flexShrink: 0,
  gap: 'var(--space-1)',
  padding: '0 var(--space-1)',
  border: '1px solid var(--border)',
  borderRadius: '999px',
  backgroundColor: 'var(--muted)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  lineHeight: 'var(--leading-normal)',
  whiteSpace: 'nowrap',
})
