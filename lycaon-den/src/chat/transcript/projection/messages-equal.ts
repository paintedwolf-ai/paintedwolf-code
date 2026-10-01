import type {
  Message,
  BlueprintMeta,
  ToolCall,
  WorkerSummaryMeta,
  WorkflowFeedbackMeta,
} from "../../../api/types.ts";
import { citationGroundingWireEqual } from "../../grounding/citation-grounding-model.ts";

/** Compare persisted message rows across a refetch. */
export function messagesEqual(
  a: readonly Message[],
  b: readonly Message[],
): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  if (a.length === 0) return true;

  for (let i = 0; i < a.length; i++) {
    if (a[i]!.id !== b[i]!.id) return false;
    if (!messageLiveFieldsEqual(a[i]!, b[i]!)) return false;
  }
  return true;
}

/** Durable sequence orders snapshots; unsequenced deltas update only open rows. */
export function latestMessageSnapshot(
  existing: Message,
  incoming: Message,
): Message {
  const incomingSeq = incoming.seq ?? 0;
  const existingSeq = existing.seq ?? 0;
  if (incomingSeq > 0) {
    return existingSeq > incomingSeq ? existing : incoming;
  }
  const existingFinalized = existingSeq > 0 && existing.status === "complete";
  return existingFinalized ? existing : incoming;
}

/** Compare fields rendered from a persisted message row. */
export function messageLiveFieldsEqual(left: Message, right: Message): boolean {
  if (
    left.id !== right.id ||
    left.role !== right.role ||
    left.content !== right.content ||
    left.visibility !== right.visibility ||
    left.kind !== right.kind ||
    left.status !== right.status ||
    left.generating_tokens !== right.generating_tokens ||
    left.origin !== right.origin ||
    left.authority !== right.authority ||
    left.trust_tier !== right.trust_tier ||
    left.host_signal_id !== right.host_signal_id ||
    left.workflow_run_id !== right.workflow_run_id ||
    left.compaction_checkpoint !== right.compaction_checkpoint ||
    left.diet_stamp !== right.diet_stamp ||
    left.draft_version_count !== right.draft_version_count ||
    left.draft_status !== right.draft_status
  ) {
    return false;
  }
  if (!jsonMetaEqual(left.content_parts, right.content_parts)) return false;
  if (!jsonMetaEqual(left.navigation_refs, right.navigation_refs)) return false;
  if (!jsonMetaEqual(left.workflow_boundary, right.workflow_boundary)) {
    return false;
  }
  if (!jsonMetaEqual(left.index_warming, right.index_warming)) return false;
  if (!jsonMetaEqual(left.workflow_explain, right.workflow_explain)) return false;
  if (!jsonMetaEqual(left.compacted_chunk, right.compacted_chunk)) return false;
  if (
    !jsonMetaEqual(left.host_secret_redaction, right.host_secret_redaction)
  ) {
    return false;
  }
  if (!stringListsEqual(left.evidence_handles, right.evidence_handles)) {
    return false;
  }
  if (!progressCompleteEqual(left.progress_complete, right.progress_complete)) {
    return false;
  }
  if (!progressUpdateEqual(left.progress_update, right.progress_update)) {
    return false;
  }
  if (!toolCallsEqual(left.tool_calls, right.tool_calls)) return false;
  if (!jsonMetaEqual(left.tool_result, right.tool_result)) return false;
  if (!citationGroundingWireEqual(left.grounding, right.grounding)) return false;
  if (!workerSummaryEqual(left.worker_summary, right.worker_summary)) return false;
  if (!workflowFeedbackEqual(left.workflow_feedback, right.workflow_feedback)) {
    return false;
  }
  if (!stringListsEqual(left.artifact_ids, right.artifact_ids)) {
    return false;
  }
  if (!planMetaEqual(left.blueprint, right.blueprint)) return false;
  return true;
}

/** Compare blueprint state carried independently of message content. */
function planMetaEqual(a?: BlueprintMeta, b?: BlueprintMeta): boolean {
  if (!a || !b) return !a && !b;
  return (
    a.blueprint_path === b.blueprint_path &&
    a.revision === b.revision &&
    a.revision_key === b.revision_key &&
    a.status === b.status &&
    a.phase === b.phase &&
    a.phase_label === b.phase_label &&
    a.blueprint_title === b.blueprint_title &&
    a.can_approve === b.can_approve &&
    a.collapsed === b.collapsed &&
    a.show_actions === b.show_actions
  );
}

/** Compare structured metadata by identity before serialization. */
function jsonMetaEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a == null || b == null) return a == null && b == null;
  return JSON.stringify(a) === JSON.stringify(b);
}

/** Order-sensitive equality for the wire's string lists (artifact ids, codes). */
function stringListsEqual(a?: string[], b?: string[]): boolean {
  if (!a?.length && !b?.length) return true;
  if (!a || !b || a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

function workflowFeedbackEqual(
  left?: WorkflowFeedbackMeta,
  right?: WorkflowFeedbackMeta,
): boolean {
  if (left === right) return true;
  if (!left || !right) return !left && !right;
  return (
    left.phase_id === right.phase_id &&
    left.prompt === right.prompt &&
    left.response_type === right.response_type &&
    (left.answer ?? "") === (right.answer ?? "") &&
    (left.resolved_by ?? "") === (right.resolved_by ?? "") &&
    (left.purpose ?? "") === (right.purpose ?? "") &&
    JSON.stringify(left.options ?? []) === JSON.stringify(right.options ?? [])
  );
}

function progressCompleteEqual(
  left?: Message["progress_complete"],
  right?: Message["progress_complete"],
): boolean {
  if (left === right) return true;
  if (!left || !right) return !left && !right;
  return (
    left.seq === right.seq &&
    JSON.stringify(left.steps) === JSON.stringify(right.steps)
  );
}

function progressUpdateEqual(
  left?: Message["progress_update"],
  right?: Message["progress_update"],
): boolean {
  if (left === right) return true;
  if (!left || !right) return !left && !right;
  return (
    left.seq === right.seq &&
    left.initial === right.initial &&
    JSON.stringify(left.steps) === JSON.stringify(right.steps) &&
    JSON.stringify(left.changes) === JSON.stringify(right.changes) &&
    JSON.stringify(left.summary) === JSON.stringify(right.summary)
  );
}

function workerSummaryEqual(
  left?: WorkerSummaryMeta,
  right?: WorkerSummaryMeta,
): boolean {
  if (left === right) return true;
  if (!left || !right) return !left && !right;
  return (
    left.worker_id === right.worker_id &&
    left.status === right.status &&
    left.delegation_id === right.delegation_id &&
    left.leg_id === right.leg_id &&
    citationGroundingWireEqual(left.grounding, right.grounding)
  );
}

function toolCallsEqual(
  left: ToolCall[] | undefined,
  right: ToolCall[] | undefined,
): boolean {
  if (left === right) return true;
  if (!left || !right) return !left && !right;
  if (left.length !== right.length) return false;
  for (let i = 0; i < left.length; i++) {
    const lc = left[i]!;
    const rc = right[i]!;
    if (lc.id !== rc.id || lc.name !== rc.name) return false;
    if (lc.args !== rc.args && JSON.stringify(lc.args) !== JSON.stringify(rc.args)) {
      return false;
    }
    if (
      lc.extra_content !== rc.extra_content &&
      JSON.stringify(lc.extra_content) !== JSON.stringify(rc.extra_content)
    ) {
      return false;
    }
  }
  return true;
}
