import type {
  CheckpointDecisionMeta,
  ToolCall,
  ToolApprovalPayload,
  ToolResult,
} from "../../api/types.ts";
import type { PendingCheckpoint } from "./checkpoint-model.ts";

export function approvalToolName(
  payload: ToolApprovalPayload | undefined,
): string | undefined {
  return payload?.plan.presentation.tool?.trim() || undefined;
}

/** Fill missing decision fields from the sibling tool call or result. */
export function enrichCheckpointDecision(
  meta: CheckpointDecisionMeta,
  call?: ToolCall,
  toolResult?: ToolResult,
): CheckpointDecisionMeta {
  const tool =
    meta.tool?.trim() || call?.name?.trim() || toolResult?.tool?.trim() || undefined;
  let subject = meta.subject?.trim() || undefined;
  if (!subject) {
    subject =
      toolResult?.file_edit_preview?.path?.trim() ||
      (call ? approvalArgsPreview(call.args) : "") ||
      undefined;
  }
  const causing =
    meta.causing_command?.trim() || undefined;
  if (
    tool === meta.tool?.trim() &&
    subject === meta.subject?.trim() &&
    causing === meta.causing_command?.trim()
  ) {
    return meta;
  }
  return {
    ...meta,
    ...(tool ? { tool } : {}),
    ...(subject ? { subject } : {}),
    ...(causing ? { causing_command: causing } : {}),
  };
}

export function approvalCommand(
  payload: ToolApprovalPayload | undefined,
): string {
  return payload?.plan.presentation.command?.trim() || "";
}

/**
 * Argv that raised the card when the subject is something else (write root,
 * host, secret, …). Empty when the subject already is that command.
 */
export function approvalCausingCommand(
  payload: ToolApprovalPayload | undefined,
): string {
  const command = approvalCommand(payload);
  if (!command) return "";
  const labels = (payload?.plan.subject.targets ?? [])
    .map((target) => target.label.trim())
    .filter(Boolean);
  if (labels.includes(command)) return "";
  return command;
}

export function approvalHeadline(
  payload: ToolApprovalPayload | undefined,
): string {
  return payload?.plan.subject.title.trim() || "Approval needed";
}

/** `origin → destination` from presentation.location. */
export function approvalLocationLine(
  payload: ToolApprovalPayload | undefined,
): string {
  const loc = payload?.plan.presentation.location;
  const origin = loc?.origin?.trim() ?? "";
  const destination = loc?.destination?.trim() ?? "";
  if (!origin || !destination) return "";
  return `${origin} → ${destination}`;
}

/** Queue / chicklet identity: credential kind plus the location line. */
export function approvalIdentity(
  payload: ToolApprovalPayload | undefined,
): string {
  const kind =
    payload?.plan.subject.targets?.[0]?.label.trim() ||
    approvalHeadline(payload);
  const location = approvalLocationLine(payload);
  return location ? `${kind} · ${location}` : kind;
}

export function approvalArgsPreview(args: Record<string, unknown>): string {
  const lines: string[] = [];
  for (const [key, value] of Object.entries(args)) {
    if (value === undefined || value === null || value === "") continue;
    if (typeof value === "string") {
      lines.push(`${key}: ${truncatePreview(value)}`);
      continue;
    }
    if (typeof value === "number" || typeof value === "boolean") {
      lines.push(`${key}: ${String(value)}`);
      continue;
    }
    try {
      lines.push(`${key}: ${truncatePreview(JSON.stringify(value))}`);
    } catch {
      lines.push(`${key}: [value]`);
    }
  }
  return lines.join("\n");
}

function truncatePreview(text: string, max = 480): string {
  const trimmed = text.trim();
  if (trimmed.length <= max) return trimmed;
  return `${trimmed.slice(0, max - 1)}…`;
}

/** Synthesize rich decision meta from an open pending checkpoint and sibling call. */
export function checkpointMetaFromPending(
  checkpoint: PendingCheckpoint,
  call?: ToolCall,
): CheckpointDecisionMeta {
  const toolApproval = checkpoint.tool_approval;
  const contentApply = checkpoint.content_apply;
  if (contentApply) {
    const tool = contentApply.tool?.trim() || "file_edit";
    const subject = contentApply.path?.trim() || undefined;
    return {
      checkpoint_id: checkpoint.checkpointId,
      kind: checkpoint.kind,
      status: checkpoint.status,
      tool,
      ...(subject ? { subject } : {}),
    };
  }
  if (toolApproval) {
    const tool =
      approvalToolName(toolApproval) ||
      call?.name?.trim() ||
      undefined;
    const isSecret = toolApproval.plan.subject.kind === "secret";
    const subject = isSecret
      ? approvalIdentity(toolApproval)
      : approvalCommand(toolApproval) ||
        approvalHeadline(toolApproval) ||
        (call ? approvalArgsPreview(call.args) : undefined);
    const causing = approvalCausingCommand(toolApproval) || undefined;
    const location = approvalLocationLine(toolApproval) || undefined;
    const recOption = toolApproval.plan.options?.find(
      (opt) => opt.id === toolApproval.plan.recommended_option_id,
    );
    const grant_title = recOption?.title?.trim() || undefined;
    const grant_scope = recOption?.scope;
    return {
      checkpoint_id: checkpoint.checkpointId,
      kind: checkpoint.kind,
      status: checkpoint.status,
      ...(tool ? { tool } : {}),
      ...(subject ? { subject } : {}),
      ...(causing ? { causing_command: causing } : {}),
      ...(location ? { location } : {}),
      ...(grant_title ? { grant_title } : {}),
      ...(grant_scope ? { grant_scope } : {}),
    };
  }
  const tool = call?.name?.trim() || undefined;
  const subject = call?.args ? approvalArgsPreview(call.args) : undefined;
  return {
    checkpoint_id: checkpoint.checkpointId,
    kind: checkpoint.kind,
    status: checkpoint.status,
    ...(tool ? { tool } : {}),
    ...(subject ? { subject } : {}),
  };
}
