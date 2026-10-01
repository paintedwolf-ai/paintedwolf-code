import type {
  CheckpointEvent,
  CheckpointKind,
  CheckpointStatus,
  ContentApplyPayload,
  ToolApprovalPayload,
} from "../../api/types.ts";

export type PendingCheckpoint = {
  checkpointId: string;
  sessionId: string;
  kind: CheckpointKind;
  status: CheckpointStatus;
  issuedAt: string;
  tool_approval?: ToolApprovalPayload;
  content_apply?: ContentApplyPayload;
};

export function pendingFromEvent(event: CheckpointEvent): PendingCheckpoint {
  return {
    checkpointId: event.id,
    sessionId: event.session_id,
    kind: event.kind,
    status: event.status,
    issuedAt: event.issued_at,
    tool_approval: event.tool_approval,
    content_apply: event.content_apply,
  };
}

export function upsertPendingCheckpoint(
  list: PendingCheckpoint[],
  event: CheckpointEvent,
): PendingCheckpoint[] {
  const without = list.filter((p) => p.checkpointId !== event.id);
  if (event.status !== "pending") {
    return without;
  }
  return [...without, pendingFromEvent(event)];
}

export function checkpointToolCallId(
  checkpoint: PendingCheckpoint,
): string {
  return (
    checkpoint.content_apply?.tool_call_id?.trim() ||
    checkpoint.tool_approval?.tool_call_id?.trim() ||
    ""
  );
}
