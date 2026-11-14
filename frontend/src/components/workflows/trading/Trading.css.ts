import { style } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

const mobile = `(max-width: ${breakpoints.sm - 1}px)`

/** The two analysts side by side; stacked on a phone. */
export const sideBySide = style({
  'display': 'grid',
  'gridTemplateColumns': 'repeat(2, minmax(0, 1fr))',
  'gap': 'var(--space-3)',
  '@media': { [mobile]: { gridTemplateColumns: 'minmax(0, 1fr)' } },
})

export const column = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const tableWrap = style({
  overflowX: 'auto',
  maxWidth: '100%',
})

export const table = style({
  width: '100%',
  borderCollapse: 'collapse',
  fontSize: 'var(--text-7)',
  fontVariantNumeric: 'tabular-nums',
})

export const cell = style({
  padding: 'var(--space-1) var(--space-2)',
  borderBottom: '1px solid var(--border)',
  textAlign: 'left',
  verticalAlign: 'top',
  whiteSpace: 'nowrap',
})

export const wrapCell = style([cell, { whiteSpace: 'normal', minWidth: '12rem' }])

export const numeric = style([cell, { textAlign: 'right' }])

export const positive = style({ color: 'var(--success)' })

export const negative = style({ color: 'var(--danger)' })

export const mono = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-8)',
  wordBreak: 'break-all',
})

/** The exact order a person confirms, set apart from everything else. */
export const orderTicket = style({
  border: '2px solid var(--warning)',
  borderRadius: 'var(--radius-medium)',
  padding: 'var(--space-3)',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const ticketGrid = style({
  display: 'grid',
  gridTemplateColumns: 'max-content minmax(0, 1fr)',
  gap: 'var(--space-1) var(--space-3)',
  fontSize: 'var(--text-7)',
})

export const metricGrid = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fill, minmax(12rem, 1fr))',
  gap: 'var(--space-2)',
})

export const metric = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const metricValue = style({
  fontSize: 'var(--text-5)',
  fontWeight: 600,
  fontVariantNumeric: 'tabular-nums',
})

export const reasons = style({
  margin: 0,
  paddingLeft: 'var(--space-4)',
  fontSize: 'var(--text-7)',
})

/** A Trading research tab's pane, which scrolls on its own. */
export const tabPane = style({
  height: '100%',
  overflowY: 'auto',
  padding: 'var(--space-4)',
  boxSizing: 'border-box',
})
