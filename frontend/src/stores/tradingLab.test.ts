import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  TradingBook,
  TradingBookMetricsSchema,
  TradingCandidateSchema,
  TradingOrderSchema,
  TradingPositionSchema,
  TradingPositionState,
} from '~/generated/proto/leapmux/v1/trading_lab_pb'
import { WorkflowStepSchema, WorkflowTemplateSchema } from '~/generated/proto/leapmux/v1/workflow_pb'
import {
  auditFindings,
  candidatesBySource,
  evaluationRows,
  isTradingTemplate,
  labelOwner,
  money,
  positionPnl,
  secondsToConfirm,
  splitMetrics,
} from './tradingLab'

describe('isTradingTemplate', () => {
  it('recognizes a template by the lab executors it runs', () => {
    const lab = create(WorkflowTemplateSchema, { steps: [create(WorkflowStepSchema, { key: 'snapshot', executor: 'tradinglab.snapshot' })] })
    const other = create(WorkflowTemplateSchema, { steps: [create(WorkflowStepSchema, { key: 'build', roleKey: 'builder' })] })
    expect(isTradingTemplate(lab)).toBe(true)
    expect(isTradingTemplate(other)).toBe(false)
    expect(isTradingTemplate(undefined)).toBe(false)
  })
})

describe('money', () => {
  it('prints signed dollars with cents', () => {
    expect(money(12.5)).toBe('$12.50')
    expect(money(-3)).toBe('-$3.00')
  })
})

describe('positionPnl', () => {
  it('uses the realized result once finished and the bid mark while open', () => {
    expect(positionPnl(create(TradingPositionSchema, { state: TradingPositionState.EXPIRED, realizedPnl: -40, lastMark: 99, cost: 100 }))).toBe(-40)
    expect(positionPnl(create(TradingPositionSchema, { state: TradingPositionState.OPEN, lastMark: 130, cost: 100 }))).toBe(30)
    expect(positionPnl(create(TradingPositionSchema, { state: TradingPositionState.NOT_FILLED, cost: 0 }))).toBe(0)
  })
})

describe('candidatesBySource', () => {
  it('splits the side-by-side columns by analyst', () => {
    const split = candidatesBySource([
      create(TradingCandidateSchema, { id: 'a', source: TradingBook.BLIND }),
      create(TradingCandidateSchema, { id: 'b', source: TradingBook.CONTRARIAN }),
      create(TradingCandidateSchema, { id: 'c', source: TradingBook.BLIND }),
    ])
    expect(split.blind.map(c => c.id)).toEqual(['a', 'c'])
    expect(split.contrarian.map(c => c.id)).toEqual(['b'])
  })
})

describe('labelOwner', () => {
  it('names the analyst only once the run reveals the label', () => {
    expect(labelOwner({ 'Submission A': 'CONTRARIAN' }, 'Submission A')).toBe('Contrarian analyst')
    expect(labelOwner({}, 'Submission B')).toBe('not revealed')
  })
})

describe('splitMetrics', () => {
  it('keeps the live book apart from the simulated ones', () => {
    const metrics = [TradingBook.LIVE, TradingBook.BLIND_INVERSE, TradingBook.BLIND, TradingBook.CONTRARIAN].map(book => create(TradingBookMetricsSchema, { book }))
    const { paper, live } = splitMetrics(metrics)
    expect(paper.map(m => m.book)).toEqual([TradingBook.BLIND, TradingBook.CONTRARIAN, TradingBook.BLIND_INVERSE])
    expect(live?.book).toBe(TradingBook.LIVE)
  })
})

describe('secondsToConfirm', () => {
  it('counts down to the confirmation deadline and stops at zero', () => {
    const order = create(TradingOrderSchema, { confirmByMs: 10_000n })
    expect(secondsToConfirm(order, 4_500)).toBe(5)
    expect(secondsToConfirm(order, 20_000)).toBe(0)
  })
})

describe('auditFindings', () => {
  it('lists each finding with the checks it failed', () => {
    const rows = auditFindings(JSON.stringify({
      findings: [{ label: 'Submission A', recommendation_id: 'r1', verdict: 'fail', issues: ['quote moved'], checks: { contract_exists: true, liquidity_adequate: false } }],
    }))
    expect(rows).toEqual([{ label: 'Submission A', recommendationId: 'r1', verdict: 'fail', issues: ['quote moved'], failedChecks: ['liquidity adequate'] }])
  })

  it('yields nothing for an unreadable document', () => {
    expect(auditFindings('{')).toEqual([])
  })
})

describe('evaluationRows', () => {
  it('flattens every scored recommendation of every submission', () => {
    const rows = evaluationRows(JSON.stringify({
      submissions: [
        { label: 'Submission A', recommendations: [{ recommendation_id: 'r1', verdict: 'strong', notes: '', scores: { evidence: 5 } }] },
        { label: 'Submission B', recommendations: [] },
      ],
    }))
    expect(rows).toEqual([{ label: 'Submission A', recommendationId: 'r1', verdict: 'strong', notes: '', scores: { evidence: 5 } }])
  })
})
