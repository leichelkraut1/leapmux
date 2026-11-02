import { style } from '@vanilla-extract/css'

// Keep the outcome and first part of the action on one line. The details
// disclosure and copy action retain the complete command and rationale.
export const reviewHeadline = style({
  flex: 1,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const reviewHeaderActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  flexShrink: 0,
})

export const reviewDetails = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const reviewFullTitle = style({
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
})
