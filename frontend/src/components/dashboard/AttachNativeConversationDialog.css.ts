import { style } from '@vanilla-extract/css'

export const fields = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  minWidth: 0,
})

export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
})

export const fieldLabel = style({
  fontWeight: 500,
})

export const description = style({
  margin: 0,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  overflowWrap: 'anywhere',
})

export const worktreeTrigger = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  width: '100%',
  minWidth: 0,
  textAlign: 'left',
})

export const triggerLabel = style({
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const worktreeMenu = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const worktreeOptions = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const error = style({
  margin: 0,
  color: 'var(--danger)',
  overflowWrap: 'anywhere',
})
