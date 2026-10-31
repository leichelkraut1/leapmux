import { style } from '@vanilla-extract/css'

export const viewer = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minWidth: 0,
  minHeight: 0,
  gap: 'var(--space-2)',
})

export const heading = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const description = style({
  margin: 0,
  color: 'var(--muted-foreground)',
  overflowWrap: 'anywhere',
  fontSize: 'var(--text-7)',
})

export const banner = style({
  margin: 0,
  padding: 'var(--space-2) var(--space-3)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  fontSize: 'var(--text-7)',
})

export const history = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minWidth: 0,
  minHeight: 0,
  overflow: 'hidden',
})

export const historyAction = style({
  alignSelf: 'center',
  flexShrink: 0,
})

export const connectionDescription = style({
  display: 'block',
  marginTop: 'var(--space-1)',
  color: 'var(--muted-foreground)',
})

export const composer = style({
  display: 'flex',
  flexDirection: 'column',
  flexShrink: 0,
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const editor = style({
  width: '100%',
  minHeight: '4.5rem',
  maxHeight: '12rem',
  resize: 'vertical',
  font: 'inherit',
})

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
})
