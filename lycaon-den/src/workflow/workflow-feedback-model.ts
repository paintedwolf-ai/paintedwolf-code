import type { Message, PendingFeedback, WorkflowFeedbackMeta, WorkflowRun } from "../api/types.ts";

/**
 * A workflow phase awaits an open-ended answer via `request_user_feedback` / ask_user.
 * The interactive form lives in AskUserDock above the composer.
 */
export function pendingWorkflowFeedback(
  run: WorkflowRun | null | undefined,
): PendingFeedback | undefined {
  if (!run || run.status !== "running") return undefined;
  const feedback = run.ui?.pending_feedback;
  if (!feedback?.prompt?.trim()) return undefined;
  return feedback;
}

/** Mode-aware composer placeholder while an ask latch is open. */
export function askUserComposerPlaceholder(meta: {
  response_type?: string;
  options?: string[];
}): string {
	if (meta.response_type === "secret") return "Enter the secret in the protected field above…";
  const options = meta.options ?? [];
  if ((meta.response_type ?? "text") === "text" || options.length === 0) {
    return "Type your answer…";
  }
  return "Describe another answer…";
}

/**
 * Composer placeholder shown while a feedback phase awaits an answer.
 * Without card meta, the free-text placeholder applies.
 */
export function workflowFeedbackComposerPlaceholder(
  run: WorkflowRun | null | undefined,
  meta?: { response_type?: string; options?: string[] },
): string | undefined {
  if (!pendingWorkflowFeedback(run)) return undefined;
  return askUserComposerPlaceholder(meta ?? { response_type: "text" });
}

/** Latest open workflow_feedback message matching the pending phase_id. */
export function pendingAskFeedbackEntry(
  messages: readonly Message[] | null | undefined,
  phaseId: string | undefined,
): { meta: WorkflowFeedbackMeta; entryKey: string; runId?: string } | undefined {
  const phase = (phaseId ?? "").trim();
  if (!phase || !messages?.length) return undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    const meta = msg?.workflow_feedback;
    if (!meta || meta.phase_id !== phase) continue;
    if ((meta.answer ?? "").trim()) continue;
    return {
      meta,
      entryKey: msg.id,
      runId: msg.workflow_run_id,
    };
  }
  return undefined;
}

function sameStrings(
  a: readonly string[] | undefined,
  b: readonly string[] | undefined,
): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  return a.every((value, i) => value === b[i]);
}

/** True when two dock metas would render the same question. */
export function sameAskDockMeta(
  a: WorkflowFeedbackMeta,
  b: WorkflowFeedbackMeta,
): boolean {
  return (
    a.phase_id === b.phase_id &&
    a.prompt === b.prompt &&
    a.response_type === b.response_type &&
    a.allow_other === b.allow_other &&
    a.artifact_id === b.artifact_id &&
    a.purpose === b.purpose &&
    sameStrings(a.options, b.options) &&
    sameStrings(a.artifact_ids, b.artifact_ids) &&
    a.secret?.name === b.secret?.name &&
    a.secret?.purpose === b.secret?.purpose &&
    a.secret?.scope === b.secret?.scope &&
    a.secret?.agent_use_ttl_ms === b.secret?.agent_use_ttl_ms
  );
}

/**
 * Dock meta whose identity survives transcript churn. The dock is keyed on this
 * object and holds the unsent pick locally, so a new identity for an unchanged
 * question (as when an optimistic answer stamp drops the source row) disposes it.
 */
export function retainAskDockMeta(
  previous: WorkflowFeedbackMeta | undefined,
  next: WorkflowFeedbackMeta | undefined,
  phaseId: string | null,
): WorkflowFeedbackMeta | undefined {
  if (!phaseId) return undefined;
  const held = previous?.phase_id === phaseId ? previous : undefined;
  if (next?.phase_id !== phaseId) return held;
  return held && sameAskDockMeta(held, next) ? held : next;
}

/** Build dock meta from pending latch + optional transcript message meta. */
export function askUserDockMeta(
  pending: PendingFeedback,
  entryMeta?: WorkflowFeedbackMeta,
): WorkflowFeedbackMeta {
  if (entryMeta && entryMeta.phase_id === pending.phase_id) {
    return entryMeta;
  }
  return {
    phase_id: pending.phase_id,
    prompt: pending.prompt,
    response_type: pending.response_type ?? "text",
    options: pending.options,
    allow_other: pending.allow_other,
    artifact_id: pending.artifact_id,
    artifact_ids: pending.artifact_ids,
    purpose: pending.purpose,
    secret: pending.secret,
  };
}
