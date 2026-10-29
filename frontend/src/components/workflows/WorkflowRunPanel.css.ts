import { style } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

const mobile = `(max-width: ${breakpoints.sm - 1}px)`

/**
 * Docked to the right edge and NOT modal: the Lead's chat stays visible and
 * usable beside it. Full width on a phone, where there is no "beside".
 */
export const panel = style({
  'position': 'fixed',
  'top': 0,
  'right': 0,
  'bottom': 0,
  'width': '440px',
  'maxWidth': '100%',
  'zIndex': 20,
  'display': 'flex',
  'flexDirection': 'column',
  'backgroundColor': 'var(--background)',
  'borderLeft': '1px solid var(--border)',
  'boxShadow': 'var(--shadow-large)',
  'paddingTop': 'env(safe-area-inset-top, 0px)',
  'paddingBottom': 'env(safe-area-inset-bottom, 0px)',
  '@media': {
    [mobile]: { width: '100%', borderLeft: 'none' },
  },
})

export const header = style({
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  padding: 'var(--space-3) var(--space-4)',
  borderBottom: '1px solid var(--border)',
})

export const heading = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

export const name = style({
  margin: 0,
  fontSize: 'var(--text-6)',
  fontWeight: 600,
})

export const body = style({
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
  scrollbarGutter: 'stable',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-4)',
  padding: 'var(--space-3) var(--space-4) var(--space-6)',
})

export const section = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const sectionTitle = style({
  margin: 0,
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: 'var(--muted-foreground)',
})

export const events = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  listStyle: 'none',
  margin: 0,
  padding: 0,
  fontSize: 'var(--text-8)',
})

export const event = style({
  display: 'grid',
  gridTemplateColumns: '4.5rem 4.5rem minmax(0, 1fr)',
  gap: 'var(--space-2)',
  overflowWrap: 'anywhere',
})

export const eventError = style({
  color: 'var(--danger)',
})
