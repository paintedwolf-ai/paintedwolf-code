import type { ActivityEvent, BackgroundProcessTerminationReason, Message, TurnLoadTrigger } from "../../api/types.ts";
import type { CardAction } from "../action/action-merge.ts";
import { formatActionRow } from "../action/action-format.ts";
import {
  getBackgroundProcessSnapshot,
  getRecordedBackgroundTerminationReason,
  hasHydratedBackgroundProcesses,
} from "../tool/background-process-store.ts";
import type { ToolPartStatus, ToolPartView } from "../tool/tool-part-model.ts";
import { findToolResult, isInternalTranscriptUserMessage } from "../transcript/projection/message-transcript.ts";
import { formatBackgroundTerminationReason } from "../../project/background-termination-reason.ts";

export const TERMINAL_WAIT_DELIVERY_LABEL = "Command finished · waiting for result delivery";
export const RUNNING_COMMAND_WAIT_LABEL = "Waiting for a command to finish";

const DECIDING_LABELS: Record<TurnLoadTrigger, string> = {
  turn: "Local AI is deciding what to load",
  request: "Local AI is matching tools",
  lookup: "Local AI is matching skills",
  tool_event: "Local AI is choosing a skill for this tool",
  visibility: "Updating available tools and skills",
};

export function formatDecidingActivity(activity: ActivityEvent): string {
  return DECIDING_LABELS[activity.decision_trigger ?? "turn"];
}

export function formatIndexingCredentialsActivity(activity: ActivityEvent): string | undefined {
  if (activity.kind !== "indexing_credentials" || !activity.indexing) return undefined;
  const { root_label, directories_visited, credential_files_read } = activity.indexing;
  const folders = `${directories_visited} ${directories_visited === 1 ? "folder" : "folders"}`;
  const files = `${credential_files_read} ${credential_files_read === 1 ? "file" : "files"}`;
  return `Checking credential files in ${root_label} · ${folders} · ${files}`;
}

export type WaitConditionSpec = {
  triggers: string[];
  processDoneHandles: string[];
  hasUnscopedProcessDone: boolean;
  otherTriggers: string[];
};

export function extractWaitConditionSpec(args?: Record<string, unknown>): WaitConditionSpec {
  const raw = Array.isArray(args?.conditions) ? args.conditions : [];
  const triggers: string[] = [];
  const processDoneHandles: string[] = [];
  let hasUnscopedProcessDone = false;
  const otherTriggers: string[] = [];

  for (const entry of raw) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
    const rec = entry as { kind?: unknown; handles?: unknown };
    const kind = typeof rec.kind === "string" ? rec.kind.trim() : "";
    if (!kind) continue;
    triggers.push(kind);
    if (kind === "process_done") {
      const handles = Array.isArray(rec.handles)
        ? rec.handles.flatMap((h) => (typeof h === "string" && h.trim() ? [h.trim()] : []))
        : [];
      if (handles.length === 0) hasUnscopedProcessDone = true;
      else processDoneHandles.push(...handles);
    } else {
      otherTriggers.push(kind);
    }
  }
  return { triggers, processDoneHandles: [...new Set(processDoneHandles)], hasUnscopedProcessDone, otherTriggers };
}

export type ObservedHandleState = "running" | "terminal" | "unknown";

export function observeBackgroundHandleState(
  sessionId: string | undefined,
  handle: string,
  messages: readonly Message[],
): ObservedHandleState {
  const sid = sessionId?.trim();
  const hid = handle.trim();
  if (!sid || !hid) return "unknown";

  const snapshot = getBackgroundProcessSnapshot(sid, hid);
  if (snapshot) return snapshot.running ? "running" : "terminal";

  let matchedMessage = false;
  let messageRunning = false;
  for (let i = messages.length - 1; i >= 0; i--) {
    const result = messages[i]?.tool_result;
    if (!result) continue;
    if (result.process?.handle?.trim() === hid) {
      matchedMessage = true;
      messageRunning = result.process.running === true;
      break;
    }
    const activity = result.command_program?.activities?.find((item) => item.job_handle?.trim() === hid);
    if (activity) {
      matchedMessage = true;
      messageRunning = activity.state === "running" || activity.state === "waiting";
      break;
    }
  }
  if (!matchedMessage) return "unknown";
  if (getRecordedBackgroundTerminationReason(sid, hid) || hasHydratedBackgroundProcesses(sid)) {
    return "terminal";
  }
  return messageRunning ? "running" : "terminal";
}

export type ResolvedWaitTarget = {
  state: "running_target" | "pending_delivery" | "neutral" | "condition_default";
  label: string;
};

export function resolveWaitTargetLabel(
  args: Record<string, unknown> | undefined,
  sessionId: string | undefined,
  messages: readonly Message[],
): ResolvedWaitTarget {
  const spec = extractWaitConditionSpec(args);
  if (spec.processDoneHandles.length === 0) {
    return { state: "condition_default", label: "" };
  }
  const states = spec.processDoneHandles.map((handle) =>
    observeBackgroundHandleState(sessionId, handle, messages),
  );
  if (spec.otherTriggers.length === 0 && !spec.hasUnscopedProcessDone) {
    if (states.every((state) => state === "terminal")) {
      return { state: "pending_delivery", label: TERMINAL_WAIT_DELIVERY_LABEL };
    }
    if (states.every((state) => state === "running")) {
      return { state: "running_target", label: RUNNING_COMMAND_WAIT_LABEL };
    }
  }
  const reason = typeof args?.reason === "string" ? args.reason.trim() : "";
  return { state: "neutral", label: reason || "Waiting" };
}

export function isWaitResultSettled(part: Pick<ToolPartView, "status" | "completion">): boolean {
  return part.status !== "running" && part.completion?.state !== "parked";
}

export function isPendingWaitPart(
  part: Pick<ToolPartView, "tool" | "toolCallId" | "status" | "completion">,
  activities?: readonly ActivityEvent[],
): boolean {
  if (part.tool.trim().toLowerCase() !== "wait") return false;
  if (part.status === "running") return true;
  if (part.completion?.state !== "parked") return false;
  const callId = part.toolCallId.trim();
  return (activities ?? []).some(
    (a) =>
      a.status === "active" &&
      a.kind === "awaiting_wake" &&
      a.tool_name?.trim().toLowerCase() === "wait" &&
      a.tool_call_id?.trim() === callId,
  );
}

/** Active host phase blocking a session handoff, ranked highest-priority first. */
export function resolveBlockingHostPhaseLabel(
  activities: readonly ActivityEvent[] | undefined,
  sessionId?: string,
): string | undefined {
  const sid = sessionId?.trim();
  const active = (activities ?? []).filter(
    (a) => a.status === "active" && (!sid || !a.session_id?.trim() || a.session_id.trim() === sid),
  );
  const deciding = active.find((a) => a.kind === "deciding");
  if (deciding) return formatDecidingActivity(deciding);
  const indexing = active.find((a) => a.kind === "indexing_credentials" && a.indexing);
  if (indexing) return formatIndexingCredentialsActivity(indexing);
  if (active.some((a) => a.kind === "preparing_context")) return "Preparing context";
  if (active.some((a) => a.kind === "starting_workflow")) return "Starting workflow";
  return undefined;
}

export function hasImmediateWaitTransition(
  sessionId: string | undefined,
  messages: readonly Message[],
  activities: readonly ActivityEvent[] | undefined,
): boolean {
  const sid = sessionId?.trim();
  const scoped = (activities ?? []).filter(
    (a) => a.status === "active" && (!sid || !a.session_id?.trim() || a.session_id.trim() === sid),
  );
  if (scoped.some((a) => a.kind === "indexing_credentials" && a.indexing !== undefined)) {
    return true;
  }
  let turnStart = 0;
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.role === "user" && !isInternalTranscriptUserMessage(messages[i]!)) {
      turnStart = i;
      break;
    }
  }
  for (let i = turnStart; i < messages.length; i++) {
    const msg = messages[i];
    if (msg?.role !== "assistant" || !msg.tool_calls?.length) continue;
    for (const call of msg.tool_calls) {
      if (call.name?.trim().toLowerCase() !== "wait") continue;
      const result = findToolResult(messages, msg.id, call.id);
      const parked = result?.tool_result?.completion?.state === "parked";
      if (result && !parked) continue;
      if (parked && !scoped.some((a) => a.kind === "awaiting_wake" && a.tool_call_id?.trim() === call.id.trim())) {
        continue;
      }
      if (resolveBlockingHostPhaseLabel(scoped, sid)) return true;
      const resolved = resolveWaitTargetLabel(call.args, sid, messages);
      if (resolved.state === "pending_delivery") return true;
    }
  }
  return false;
}

export function resolveLiveProcessCardPresentation(
  sessionId: string | undefined,
  jobHandle: string | undefined,
  action: CardAction,
  fallbackReason?: BackgroundProcessTerminationReason,
): { status: ToolPartStatus; outcome: string } {
  const baseStatus: ToolPartStatus = action.status === "done" ? "completed" : action.status;
  const baseOutcome = formatActionRow(action);
  const sid = sessionId?.trim();
  const hid = jobHandle?.trim();
  if (!sid || !hid || baseStatus === "error") {
    return { status: baseStatus, outcome: baseOutcome };
  }
  const snapshot = getBackgroundProcessSnapshot(sid, hid);
  if (snapshot) {
    if (snapshot.running) return { status: "running", outcome: baseOutcome || "Running" };
    const reason = snapshot.terminationReason;
    if (reason && reason !== "exited") {
      return {
        status: reason === "timed_out" ? "error" : "completed",
        outcome: formatBackgroundTerminationReason(reason),
      };
    }
    const code = snapshot.exitCode;
    if (code !== undefined && code !== 0) {
      return { status: "error", outcome: `Failed · exit ${code}` };
    }
    return { status: "completed", outcome: code !== undefined ? `Completed · exit ${code}` : "Completed" };
  }
  const recorded = fallbackReason ?? getRecordedBackgroundTerminationReason(sid, hid);
  if (recorded) {
    return { status: "completed", outcome: formatBackgroundTerminationReason(recorded) };
  }
  if (hasHydratedBackgroundProcesses(sid)) {
    return { status: "completed", outcome: "Finished" };
  }
  return { status: baseStatus, outcome: baseOutcome };
}
