import { LycaonApiError } from "../api/http.ts";
import type { WorkflowFeedbackMeta } from "../api/types.ts";

/** Presentation state for a workflow / ask_user question card. */
export type WorkflowFeedbackCardView =
  | { kind: "open" }
  | { kind: "answered"; resolvedBy: string }
  | { kind: "closed" };

/**
 * Derive card view from wire meta + the session's live pending latch.
 *
 * `activePendingPhaseId`:
 * - `undefined` — latch unknown (omit prop); unanswered cards stay open
 * - `null` — latch loaded, nothing pending; unanswered cards stay open until
 *   the resolve stamp arrives
 * - `string` — that phase is the open ask; other unanswered cards close
 *
 * A stamped answer always wins, so a late submit still renders as answered.
 */
export function workflowFeedbackCardView(
  meta: Pick<WorkflowFeedbackMeta, "phase_id" | "answer" | "resolved_by">,
  activePendingPhaseId?: string | null,
): WorkflowFeedbackCardView {
  const answer = (meta.answer ?? "").trim();
  if (answer) {
    const resolvedBy = (meta.resolved_by ?? "user").trim() || "user";
    return { kind: "answered", resolvedBy };
  }
  const pending = activePendingPhaseId;
  if (typeof pending === "string" && pending.trim() && pending.trim() !== meta.phase_id) {
    return { kind: "closed" };
  }
  return { kind: "open" };
}

export function workflowFeedbackCardLabel(view: WorkflowFeedbackCardView): string {
  switch (view.kind) {
    case "open":
      return "Your input";
    case "answered":
      return "Answered";
    case "closed":
      return "No longer needed";
  }
}

export function workflowFeedbackCardHint(view: WorkflowFeedbackCardView): string | undefined {
  if (view.kind === "closed") {
    return "This question is closed. The agent has moved on.";
  }
  return undefined;
}

/** Summary line for the closed ask_user chicklet (answer preferred over prompt). */
export function workflowFeedbackChickletDetail(
  prompt: string | undefined,
  answer: string | undefined,
): string | undefined {
  const a = (answer ?? "").trim();
  if (a) return a;
  const p = (prompt ?? "").trim();
  return p || undefined;
}

/** Structured wire codes when the ask latch is gone or was never a live choice ask. */
const FEEDBACK_GONE_CODES = new Set([
  "feedback_not_pending",
  "decision_not_pending",
  "not_choice_phase",
]);

/** True when resolve failed because the coordinator→human latch cannot accept this submit. */
export function isFeedbackNoLongerPendingError(err: unknown): boolean {
  return err instanceof LycaonApiError && Boolean(err.code && FEEDBACK_GONE_CODES.has(err.code));
}

/** Display string for a successful card submit (optimistic / stamp echo). */
export function composeFeedbackSubmitAnswer(
  responseType: string | undefined,
  text: string,
  choices: string[],
): string {
  if ((responseType ?? "text") === "text") {
    return text.trim();
  }
  return choices.map((choice) => choice.trim()).filter(Boolean).join(", ");
}
