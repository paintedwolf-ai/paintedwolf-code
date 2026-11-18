import type { Message } from "../../api/types.ts";
import type { TranscriptLayout } from "../transcript/layout/transcript-layout.ts";
import { COORDINATOR_INTERNAL_TOOLS, LONG_RUNNING_COORDINATOR_TOOLS } from "./tool-presentation.generated.ts";

export const FORM_PROGRAM_TOOLS: ReadonlySet<string> = new Set([
  "command",
  "summarize",
  "survey_repo",
  "code_rewrite",
]);

export const PROGRAM_ACTIVITY_TOOLS: ReadonlySet<string> = new Set([
  ...FORM_PROGRAM_TOOLS,
  "verify",
]);

const MAX_SESSIONS = 8;
const MAX_CALLS_PER_SESSION = 2048;
const shown = new Map<string, Set<string>>();

export function noteToolCallShownInFlight(sessionId: string, callId: string): void {
  const calls = shown.get(sessionId) ?? new Set<string>();
  shown.delete(sessionId);
  shown.set(sessionId, calls);
  calls.delete(callId);
  calls.add(callId);
  if (calls.size > MAX_CALLS_PER_SESSION) calls.delete(calls.values().next().value!);
  if (shown.size > MAX_SESSIONS) shown.delete(shown.keys().next().value!);
}

export function wasToolCallShownInFlight(sessionId: string, callId: string): boolean {
  if (!shown.get(sessionId)?.has(callId)) return false;
  noteToolCallShownInFlight(sessionId, callId);
  return true;
}

const LONG_RUNNING_TOOL_SET = new Set<string>(LONG_RUNNING_COORDINATOR_TOOLS);
const COORDINATOR_INTERNAL_TOOL_SET = new Set<string>(
  COORDINATOR_INTERNAL_TOOLS,
);

/** Settled benign results remain visible for lifecycle tools. */
function isLifecycleVisibleTool(tool: string): boolean {
  const name = tool.toLowerCase();
  return LONG_RUNNING_TOOL_SET.has(name) && !PROGRAM_ACTIVITY_TOOLS.has(name);
}

/** Tools that orient the coordinator privately. */
export function isCoordinatorInternalTool(toolName: string): boolean {
  return COORDINATOR_INTERNAL_TOOL_SET.has(toolName.toLowerCase());
}

/** Parent chat hides orientation tools; worker drawer shows full child-session activity. */
export function shouldHideCoordinatorInternalTool(
  toolName: string,
  layout: TranscriptLayout = "chat",
): boolean {
  return layout === "chat" && isCoordinatorInternalTool(toolName);
}

/** Host-stamped quiet chrome (`tool_result.ui_visibility === "benign"`). */
export function isBenignToolResult(resultMsg?: Message): boolean {
  return resultMsg?.tool_result?.ui_visibility === "benign";
}

type ToolPartTranscriptOmitOptions = {
  verboseMode: boolean;
  layout?: TranscriptLayout;
  shownInFlight?: boolean;
};

type ToolTranscriptVisibility =
  | "visible"
  | "verbose_only"
  | "hidden";

/** Resolve transcript visibility before a tool surface mounts. */
function toolTranscriptVisibility(
  resultMsg: Message | undefined,
  toolName: string,
  layout: TranscriptLayout,
  shownInFlight: boolean,
): ToolTranscriptVisibility {
  if (shouldHideCoordinatorInternalTool(toolName, layout)) return "hidden";
  if (layout === "worker") return "visible";
  if (shownInFlight || isLifecycleVisibleTool(toolName)) return "visible";
  if (!resultMsg) return "verbose_only";
  const result = resultMsg.tool_result;
  if (result?.process?.running && result.process.handle?.trim()) return "visible";
  if (result?.file_edit_previews?.some((edit) => edit.path.trim() !== "") || (result?.file_edits_omitted ?? 0) > 0) return "visible";
  return isBenignToolResult(resultMsg) ? "verbose_only" : "visible";
}

/** Quiet chat defers ordinary tools until they settle. */
export function shouldOmitToolPartFromTranscript(
  resultMsg: Message | undefined,
  toolName: string,
  options: ToolPartTranscriptOmitOptions,
): boolean {
  const layout = options.layout ?? "chat";
  const visibility = toolTranscriptVisibility(resultMsg, toolName, layout, options.shownInFlight ?? false);
  if (visibility === "visible") return false;
  if (visibility === "hidden") return true;
  return !options.verboseMode;
}

/** Whether the row is placed at the assistant call. */
export function toolDrawsWhileRunning(toolName: string, options: ToolPartTranscriptOmitOptions): boolean {
  return !shouldOmitToolPartFromTranscript(undefined, toolName, options);
}
