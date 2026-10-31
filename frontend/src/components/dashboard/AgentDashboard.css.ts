import { style, styleVariants } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

const mobile = `(max-width: ${breakpoints.sm - 1}px)`

export const dashboard = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  flex: 1,
  minWidth: 0,
  minHeight: 0,
})

export const description = style({
  margin: 0,
  color: 'var(--muted-foreground)',
})

export const summary = style({
  'display': 'grid',
  'gridTemplateColumns': 'repeat(4, minmax(0, 1fr))',
  'gap': 'var(--space-2)',
  'margin': 0,
  '@media': {
    [mobile]: { gridTemplateColumns: 'repeat(2, minmax(0, 1fr))' },
  },
})

export const summaryItem = style({
  padding: 'var(--space-3)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  fontSize: 'var(--text-7)',
})

export const count = style({
  margin: 0,
  fontSize: 'var(--text-4)',
  fontWeight: 600,
  fontVariantNumeric: 'tabular-nums',
})

export const controls = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  minWidth: 0,
})

export const search = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  margin: 0,
  fontSize: 'var(--text-7)',
})

export const resultCount = style({
  margin: 0,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  fontVariantNumeric: 'tabular-nums',
})

export const results = style({
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
  scrollbarGutter: 'stable',
})

export const agentList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  listStyle: 'none',
  margin: 0,
  padding: 0,
})

export const agentRow = style({
  'display': 'grid',
  'gridTemplateColumns': 'minmax(0, 1.2fr) minmax(0, 1fr) 10rem 6rem',
  'gridTemplateAreas': '"identity location status action"',
  'alignItems': 'center',
  'gap': 'var(--space-3)',
  'padding': 'var(--space-3)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-medium)',
  '@media': {
    [mobile]: {
      gridTemplateColumns: 'minmax(0, 1fr) 6rem',
      gridTemplateAreas: '"identity identity" "location location" "status action"',
    },
  },
})

export const identity = style({
  gridArea: 'identity',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

export const agentTitle = style({
  margin: 0,
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  lineHeight: 1.5,
})

export const secondary = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})

export const location = style({
  gridArea: 'location',
  minWidth: 0,
  margin: 0,
  fontSize: 'var(--text-7)',
})

export const locationLine = style({
  display: 'flex',
  flexDirection: 'column',
  minWidth: 0,
})

export const locationValue = style({
  margin: 0,
  overflowWrap: 'anywhere',
})

export const statusBlock = style({
  gridArea: 'status',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
})

export const statusDetail = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  overflowWrap: 'anywhere',
})

export const status = style({
  fontSize: 'var(--text-7)',
  fontWeight: 600,
})

export const statusColor = styleVariants({
  working: { color: 'var(--primary)' },
  waiting: { color: 'var(--warning)' },
  idle: { color: 'var(--success)' },
  offline: { color: 'var(--muted-foreground)' },
  unknown: { color: 'var(--muted-foreground)' },
})

export const openButton = style({
  gridArea: 'action',
  width: '6rem',
  whiteSpace: 'nowrap',
})

export const empty = style({
  padding: 'var(--space-6)',
  textAlign: 'center',
  color: 'var(--muted-foreground)',
})

export const note = style({
  margin: 0,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})

export const managedSection = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  minWidth: 0,
})

export const externalSection = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  minWidth: 0,
  marginTop: 'var(--space-4)',
  paddingTop: 'var(--space-4)',
  borderTop: '1px solid var(--border)',
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  flexWrap: 'wrap',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const sectionTitle = style({
  margin: 0,
  fontSize: 'var(--text-6)',
  fontWeight: 600,
})

export const externalWorker = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: 0,
  padding: 'var(--space-3)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  overflowWrap: 'anywhere',
})

export const observationMessage = style({
  margin: 0,
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})

export const externalProcessList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  listStyle: 'none',
  margin: 0,
  padding: 0,
})

export const externalProcess = style({
  'display': 'grid',
  'gridTemplateColumns': 'minmax(0, 1fr) minmax(0, 1fr)',
  'gap': 'var(--space-2)',
  'paddingTop': 'var(--space-2)',
  'borderTop': '1px solid var(--border)',
  '@media': {
    [mobile]: { gridTemplateColumns: 'minmax(0, 1fr)' },
  },
})

export const processIdentity = style({
  display: 'flex',
  flexDirection: 'column',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

export const processObservation = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
  color: 'var(--muted-foreground)',
})

export const unobservedProcesses = style({
  fontSize: 'var(--text-7)',
})

export const processSummary = style({
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  paddingBlock: 'var(--space-2)',
})

export const serviceGroup = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  paddingTop: 'var(--space-2)',
  borderTop: '1px solid var(--border)',
})
