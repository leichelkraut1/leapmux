import { globalStyle, style } from '@vanilla-extract/css'
import { fieldTrigger } from '~/styles/shared.css'
import { breakpoints } from '~/styles/tokens'

const mobile = `(max-width: ${breakpoints.sm - 1}px)`

/** The dialog's column: toolbar, then a scrolling body. */
export const dialogBody = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  flex: 1,
  minWidth: 0,
  minHeight: 0,
})

export const toolbar = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'flex-end',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
})

export const toolbarField = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: '14rem',
  fontSize: 'var(--text-7)',
})

export const scroll = style({
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
  scrollbarGutter: 'stable',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

export const description = style({
  margin: 0,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  flexWrap: 'wrap',
  gap: 'var(--space-2)',
})

export const sectionTitle = style({
  margin: 0,
  fontSize: 'var(--text-6)',
  fontWeight: 600,
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  listStyle: 'none',
  margin: 0,
  padding: 0,
})

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  padding: 'var(--space-3)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

export const row = style({
  'display': 'grid',
  'gridTemplateColumns': 'minmax(0, 1fr) auto',
  'alignItems': 'center',
  'gap': 'var(--space-3)',
  'padding': 'var(--space-3)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-medium)',
  'minWidth': 0,
  '@media': {
    [mobile]: { gridTemplateColumns: 'minmax(0, 1fr)' },
  },
})

export const rowButton = style([row, {
  width: '100%',
  textAlign: 'left',
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  cursor: 'pointer',
  selectors: {
    '&:hover': { backgroundColor: 'var(--accent)' },
  },
}])

export const identity = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

export const title = style({
  margin: 0,
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  lineHeight: 1.5,
})

export const secondary = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})

export const inline = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
})

export const empty = style({
  padding: 'var(--space-6)',
  textAlign: 'center',
  color: 'var(--muted-foreground)',
})

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  minWidth: 0,
})

export const grid2 = style({
  'display': 'grid',
  'gridTemplateColumns': 'repeat(2, minmax(0, 1fr))',
  'gap': 'var(--space-3)',
  'alignItems': 'start',
  '@media': {
    [mobile]: { gridTemplateColumns: 'minmax(0, 1fr)' },
  },
})

export const grid3 = style({
  'display': 'grid',
  'gridTemplateColumns': 'repeat(3, minmax(0, 1fr))',
  'gap': 'var(--space-3)',
  'alignItems': 'start',
  '@media': {
    [mobile]: { gridTemplateColumns: 'minmax(0, 1fr)' },
  },
})

export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
  margin: 0,
  fontSize: 'var(--text-7)',
})

export const checkboxLabel = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-7)',
})

export const textarea = style({
  minHeight: '6rem',
  resize: 'vertical',
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-7)',
})

export const hint = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})

export const fieldset = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  margin: 0,
  padding: 'var(--space-3)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  minWidth: 0,
})

globalStyle(`${fieldset} > legend`, {
  padding: '0 var(--space-1)',
  fontWeight: 600,
  fontSize: 'var(--text-7)',
})

export const footer = style({
  display: 'flex',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
  paddingTop: 'var(--space-2)',
})

export const trigger = style([fieldTrigger, {
  width: '100%',
  padding: 'var(--space-2) var(--space-3)',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  textAlign: 'left',
  cursor: 'pointer',
  selectors: {
    '&:disabled': { opacity: 0.5, cursor: 'not-allowed' },
  },
}])

export const triggerValue = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const menu = style({
  minWidth: '14rem',
  maxHeight: '22rem',
  overflowY: 'auto',
})

export const dotOnline = style({
  background: 'var(--success)',
})

export const dotOffline = style({
  background: 'var(--danger)',
})

export const chips = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-1)',
})

export const chip = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})

export const summaryStats = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-3)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  fontVariantNumeric: 'tabular-nums',
})

export const pre = style({
  margin: 0,
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
  fontSize: 'var(--text-8)',
})

export const grow = style({
  flex: 1,
  minWidth: 0,
})
