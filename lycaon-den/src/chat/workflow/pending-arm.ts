import type { WorkflowSummary } from "../../api/types.ts";

/** Cross-pane intent: open a session with a workflow armed (e.g. Blueprints launcher). */
export type PendingArmRequest = {
  sessionId: string;
  workflow: WorkflowSummary;
};

let pending: PendingArmRequest | null = null;

export function requestArmWorkflow(req: PendingArmRequest): void {
  const sessionId = req.sessionId.trim();
  if (!sessionId) return;
  pending = { sessionId, workflow: req.workflow };
}

/** Pending arm for `sessionId`, or null if none / mismatch. Does not clear. */
export function peekArmWorkflow(sessionId: string): WorkflowSummary | null {
  const id = sessionId.trim();
  if (!pending || !id || pending.sessionId !== id) return null;
  return pending.workflow;
}

/** Drop a queued arm (after clear, successful start, or session catalog run). */
export function clearPendingArmWorkflow(): void {
  pending = null;
}
