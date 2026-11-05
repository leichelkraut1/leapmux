import { createSignal } from 'solid-js'
import { workflowErrorMessage } from '~/stores/workflows'

/**
 * One button's request: whether it is in flight, and the refusal to show
 * beside it. The engine's InvalidArgument and FailedPrecondition messages are
 * written for the user, so the error is shown as the engine said it.
 */
export function createWorkflowAction() {
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const run = async (fn: () => Promise<void>): Promise<boolean> => {
    if (busy())
      return false
    setBusy(true)
    setError(null)
    try {
      await fn()
      return true
    }
    catch (err) {
      setError(workflowErrorMessage(err))
      return false
    }
    finally {
      setBusy(false)
    }
  }
  return { busy, error, setError, run }
}
