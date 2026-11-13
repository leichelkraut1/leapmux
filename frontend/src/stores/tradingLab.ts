import type { WorkflowTone } from './workflows'
import type {
  TradingBookMetrics,
  TradingCandidate,
  TradingOrder,
  TradingPosition,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'
import type { WorkflowTemplate } from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  TradingBook,
  TradingCandidateStatus,
  TradingOrderSide,
  TradingOrderState,
  TradingPositionState,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'

/** The executors that make a template a trading research workflow. */
const LAB_EXECUTORS = ['tradinglab.snapshot', 'tradinglab.record']

/** Whether a template is a trading research workflow (runs the lab's executors). */
export function isTradingTemplate(template: Pick<WorkflowTemplate, 'steps'> | undefined): boolean {
  return !!template?.steps.some(s => LAB_EXECUTORS.includes(s.executor))
}

export const BOOK_LABELS: Record<TradingBook, string> = {
  [TradingBook.UNSPECIFIED]: 'Unknown',
  [TradingBook.BLIND]: 'Blind analyst',
  [TradingBook.CONTRARIAN]: 'Contrarian analyst',
  [TradingBook.BLIND_INVERSE]: 'Blind, opposite direction',
  [TradingBook.LIVE]: 'Live (real fills)',
}

/** The simulated books, in the order the dashboard shows them. */
export const SHADOW_BOOKS = [TradingBook.BLIND, TradingBook.CONTRARIAN, TradingBook.BLIND_INVERSE] as const

export function money(value: number): string {
  const sign = value < 0 ? '-' : ''
  return `${sign}$${Math.abs(value).toFixed(2)}`
}

export function percent(value: number, digits = 1): string {
  return `${(value * 100).toFixed(digits)}%`
}

export function shortHash(sha: string): string {
  return sha ? sha.slice(0, 12) : ''
}

export interface StatusWord {
  label: string
  tone: WorkflowTone
}

export function candidateStatusView(status: TradingCandidateStatus): StatusWord {
  switch (status) {
    case TradingCandidateStatus.ELIGIBLE:
      return { label: 'Eligible for review', tone: 'neutral' }
    case TradingCandidateStatus.REJECTED:
      return { label: 'Rejected', tone: 'danger' }
    case TradingCandidateStatus.NO_TRADE:
      return { label: 'No trade', tone: 'muted' }
    case TradingCandidateStatus.APPROVED:
      return { label: 'In approved queue', tone: 'success' }
    case TradingCandidateStatus.DISMISSED:
      return { label: 'Dismissed', tone: 'muted' }
    case TradingCandidateStatus.ORDERED:
      return { label: 'Ordered', tone: 'success' }
    default:
      return { label: 'Unknown', tone: 'muted' }
  }
}

export function orderStateView(state: TradingOrderState): StatusWord {
  switch (state) {
    case TradingOrderState.PREPARED:
      return { label: 'Awaiting your confirmation', tone: 'warning' }
    case TradingOrderState.CONFIRMED:
      return { label: 'Confirmed', tone: 'neutral' }
    case TradingOrderState.SUBMITTING:
      return { label: 'Outcome unknown: reconcile', tone: 'warning' }
    case TradingOrderState.SUBMITTED:
      return { label: 'Working at broker', tone: 'neutral' }
    case TradingOrderState.PARTIALLY_FILLED:
      return { label: 'Partially filled', tone: 'neutral' }
    case TradingOrderState.FILLED:
      return { label: 'Filled', tone: 'success' }
    case TradingOrderState.CANCELLED:
      return { label: 'Cancelled', tone: 'muted' }
    case TradingOrderState.REJECTED:
      return { label: 'Rejected by broker', tone: 'danger' }
    case TradingOrderState.EXPIRED:
      return { label: 'Expired unconfirmed', tone: 'muted' }
    case TradingOrderState.FAILED:
      return { label: 'Failed', tone: 'danger' }
    default:
      return { label: 'Unknown', tone: 'muted' }
  }
}

export function positionStateView(state: TradingPositionState): StatusWord {
  switch (state) {
    case TradingPositionState.OPEN:
      return { label: 'Open', tone: 'neutral' }
    case TradingPositionState.CLOSED:
      return { label: 'Closed', tone: 'muted' }
    case TradingPositionState.EXPIRED:
      return { label: 'Expired', tone: 'muted' }
    case TradingPositionState.NOT_FILLED:
      return { label: 'Not executable', tone: 'warning' }
    default:
      return { label: 'Unknown', tone: 'muted' }
  }
}

export function sideLabel(side: TradingOrderSide): string {
  return side === TradingOrderSide.SELL_TO_CLOSE ? 'Sell to close' : 'Buy to open'
}

/** A position's profit so far: realized when finished, marked to the bid while open. */
export function positionPnl(p: TradingPosition): number {
  switch (p.state) {
    case TradingPositionState.CLOSED:
    case TradingPositionState.EXPIRED:
      return p.realizedPnl
    case TradingPositionState.OPEN:
      return p.lastMark - p.cost
    default:
      return 0
  }
}

/** Candidates split by the analyst who made them, for the side-by-side view. */
export function candidatesBySource(candidates: readonly TradingCandidate[]): { blind: TradingCandidate[], contrarian: TradingCandidate[] } {
  return {
    blind: candidates.filter(c => c.source === TradingBook.BLIND),
    contrarian: candidates.filter(c => c.source === TradingBook.CONTRARIAN),
  }
}

/**
 * Whose a label was, in words, once the run reveals it. Until then the
 * evaluation reads as it did to the evaluator.
 */
export function labelOwner(labelSources: Readonly<Record<string, string>>, label: string): string {
  const source = labelSources[label]
  if (source === 'BLIND')
    return BOOK_LABELS[TradingBook.BLIND]
  if (source === 'CONTRARIAN')
    return BOOK_LABELS[TradingBook.CONTRARIAN]
  return 'not revealed'
}

/** Simulated and live metrics apart, so a paper result is never read as a real one. */
export function splitMetrics(metrics: readonly TradingBookMetrics[]): { paper: TradingBookMetrics[], live: TradingBookMetrics | undefined } {
  return {
    paper: SHADOW_BOOKS.map(book => metrics.find(m => m.book === book)).filter((m): m is TradingBookMetrics => !!m),
    live: metrics.find(m => m.book === TradingBook.LIVE),
  }
}

/** Seconds left to confirm a prepared order, never negative. */
export function secondsToConfirm(order: Pick<TradingOrder, 'confirmByMs'>, nowMs: number): number {
  return Math.max(0, Math.floor((Number(order.confirmByMs) - nowMs) / 1000))
}

export interface AuditFindingRow {
  label: string
  recommendationId: string
  verdict: string
  issues: string[]
  failedChecks: string[]
}

/** Reads the frozen audit document into rows; an unreadable one yields none. */
export function auditFindings(auditJson: string): AuditFindingRow[] {
  if (!auditJson)
    return []
  try {
    const doc = JSON.parse(auditJson) as { findings?: { label: string, recommendation_id: string, verdict: string, issues?: string[], checks?: Record<string, boolean> }[] }
    return (doc.findings ?? []).map(f => ({
      label: f.label,
      recommendationId: f.recommendation_id,
      verdict: f.verdict,
      issues: f.issues ?? [],
      failedChecks: Object.entries(f.checks ?? {}).filter(([, ok]) => !ok).map(([name]) => name.replaceAll('_', ' ')),
    }))
  }
  catch {
    return []
  }
}

export interface EvaluationRow {
  label: string
  recommendationId: string
  verdict: string
  notes: string
  scores: Record<string, number>
}

/** Reads the frozen evaluation document into rows; an unreadable one yields none. */
export function evaluationRows(evaluationJson: string): EvaluationRow[] {
  if (!evaluationJson)
    return []
  try {
    const doc = JSON.parse(evaluationJson) as { submissions?: { label: string, recommendations?: { recommendation_id: string, verdict: string, notes: string, scores: Record<string, number> }[] }[] }
    return (doc.submissions ?? []).flatMap(s => (s.recommendations ?? []).map(r => ({
      label: s.label,
      recommendationId: r.recommendation_id,
      verdict: r.verdict,
      notes: r.notes,
      scores: r.scores,
    })))
  }
  catch {
    return []
  }
}
