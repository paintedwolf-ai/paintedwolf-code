import type {
  Message,
  ChatContentReference,
  ToolCall,
  FileEditPreview,
  VisualArtifact,
  WorkerSummaryMeta,
  ExternalAccess,
  SkillActivation,
  ToolProcessHandle,
  ToolCompletion,
  InvocationReceipt,
  VerdictOutcome,
  HostSecretRedactionMeta,
  ToolFeedback,
  ToolResultOutcome,
} from "../../api/types.ts";
import type { TranscriptLayout } from "../transcript/layout/transcript-layout.ts";
import {
  REDACTION_FIELD_TOOL_RESULT_ARGS,
  toolCallArgsRedactionField,
  type ToolArgsRedaction,
} from "../transcript/content/redaction-spans.ts";
import { resolveChickletTitle } from "./tool-chicklet-titles.ts";
import { skillReadResource, skillResourceTitle } from "../skill/skill-card-model.ts";
import {
  COORDINATOR_INTERNAL_TOOLS,
  LONG_RUNNING_COORDINATOR_TOOLS,
  TOOL_CARD_KINDS,
  TOOL_CHICKLET_RUNNING_LABELS,
} from "./tool-presentation.generated.ts";
import {
  getBackgroundProcessSnapshot,
  hasHydratedBackgroundProcesses,
} from "./background-process-store.ts";

/** Stable transcript row identity. */
export function toolPartStableId(
  assistantMessageId: string,
  call: Pick<ToolCall, "id">,
): string {
  return `${assistantMessageId}:${call.id.trim()}`;
}

/** Host-native tool card view. */
export type ToolPartKind =
  | "read"
  | "write"
  | "command"
  | "task"
  | "skill"
  | "generic";

export type ToolPartStatus = "running" | "completed" | "error";

/** Message origins written outside this machine. */
const EXTERNALLY_AUTHORED_ORIGINS: ReadonlySet<string> = new Set([
  "retrieval",
  "peer_agent",
  "attachment",
]);

export interface ToolPartView {
  /** Assistant-row and tool-call identity. */
  id: string;
  /** Host tool-call identity. */
  toolCallId: string;
  /** Assistant row that issued this call. */
  assistantMessageId: string;
  /** Durable workflow scope for grouping; absent for an ordinary chat batch. */
  workflowRunId?: string;
  messageId: string;
  tool: string;
  kind: ToolPartKind;
  status: ToolPartStatus;
  title?: string;
  displaySubject?: string;
  args?: Record<string, unknown>;
  output?: string | null;
  outputReference?: ChatContentReference;
  argsReference?: ChatContentReference;
  argsMessageId?: string;
  error?: string | null;
  jobId?: string;
  childSessionId?: string;
  fileEdit?: FileEditPreview | null;
  visual?: VisualArtifact | null;
  /** Host-stamped exceptional external-access detail for the transcript chicklet. */
  externalAccess?: ExternalAccess | null;
  /** Whether the host marked result bytes as externally authored. */
  externallyAuthored?: boolean;
  /** Host-stamped skill activation. */
  skill?: SkillActivation | null;
  /** Persisted worker lifecycle and grounding. */
  workerSummary?: WorkerSummaryMeta;
  completion?: ToolCompletion | null;
  /** Host-stamped live process handle. */
  process?: ToolProcessHandle | null;
  /** Host-stamped accepted review verdict. */
  verdict?: VerdictOutcome | null;
  /** Contract receipt for the action shown by this card. */
  invocation?: InvocationReceipt | null;
  /** Host provenance for redacted result spans. */
  redaction?: HostSecretRedactionMeta | null;
  /** Host provenance for rendered argument spans. */
  argsRedaction?: ToolArgsRedaction | null;
  /** Host-stamped result classification. */
  outcome?: ToolResultOutcome;
  /** Host-stamped guidance codes. */
  codes?: string[];
  /** Structured guidance facts. */
  feedback?: ToolFeedback[];
}

const TOOL_PART_KINDS = new Set<string>([
  "read",
  "write",
  "command",
  "task",
  "skill",
  "generic",
]);

/** Host-declared card rendering family. */
export function classifyToolKind(toolName: string): ToolPartKind {
  const kind = TOOL_CARD_KINDS[toolName.toLowerCase()];
  return kind && TOOL_PART_KINDS.has(kind) ? (kind as ToolPartKind) : "generic";
}

const LONG_RUNNING_TOOL_SET = new Set<string>(LONG_RUNNING_COORDINATOR_TOOLS);
const COORDINATOR_INTERNAL_TOOL_SET = new Set<string>(
  COORDINATOR_INTERNAL_TOOLS,
);

/** Worker dispatch tools — TaskCard routing, roster binding, worker transcript. */
export function isTaskToolName(tool: string): boolean {
  return classifyToolKind(tool) === "task";
}

/** Settled benign results remain visible for lifecycle tools. */
function isLifecycleVisibleTool(tool: string): boolean {
  return LONG_RUNNING_TOOL_SET.has(tool.toLowerCase());
}

/** Membership for parent-chat orientation tools (see shouldHideCoordinatorInternalTool). */
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

function toolOutputText(msg?: Message): string | null {
  if (!msg) return null;
  const raw = msg.tool_result?.content ?? msg.content;
  const trimmed = raw?.trim();
  return trimmed ? trimmed : null;
}

function withHandle(
  process?: ToolProcessHandle | null,
): ToolProcessHandle | null {
  return process?.handle?.trim() ? process : null;
}

/** Live process returned before command completion. */
export function liveToolProcess(part: ToolPartView): ToolProcessHandle | null {
  return withHandle(part.process);
}

function statusFromToolResult(
  resultMsg: Message | undefined,
  output: string | null,
): ToolPartStatus {
  const invocation = resultMsg?.tool_result?.invocation;
  if (invocation?.status === "running") return "running";
  if (
    invocation?.status === "rejected" ||
    invocation?.status === "error" ||
    invocation?.status === "interrupted"
  ) return "error";
  if (invocation?.status === "completed") {
    return withHandle(resultMsg?.tool_result?.process)?.running
      ? "running"
      : "completed";
  }
  if (!output && !resultMsg?.tool_result?.content_ref) return "running";
  const outcome = resultMsg?.tool_result?.outcome;
  if (outcome === "rejected" || outcome === "error") return "error";
  // A live handle is not completion.
  if (withHandle(resultMsg?.tool_result?.process)?.running) return "running";
  return "completed";
}

/** Resolves status against the live process mirror. */
export function toolPartDisplayStatus(
  part: ToolPartView,
  sessionId?: string,
): ToolPartStatus {
  if (part.status === "error") return part.status;
  const live = liveToolProcess(part);
  if (!live) return part.status;
  const sid = sessionId?.trim();
  if (!sid) return live.running ? "running" : "completed";
  const snap = getBackgroundProcessSnapshot(sid, live.handle);
  if (snap) return snap.running ? "running" : "completed";
  if (hasHydratedBackgroundProcesses(sid)) return "completed";
  return live.running ? "running" : "completed";
}

/** Host-stamped quiet chrome (`tool_result.ui_visibility === "benign"`). */
export function isBenignToolResult(resultMsg?: Message): boolean {
  return resultMsg?.tool_result?.ui_visibility === "benign";
}

type ToolPartTranscriptOmitOptions = {
  verboseMode: boolean;
  layout?: TranscriptLayout;
};

type ToolTranscriptVisibility =
  | "visible"
  | "verbose_only"
  | "hidden";

/** Resolve transcript visibility before a tool surface mounts. */
function toolTranscriptVisibility(
  resultMsg: Message | undefined,
  toolName: string,
  layout: TranscriptLayout = "chat",
): ToolTranscriptVisibility {
  if (shouldHideCoordinatorInternalTool(toolName, layout)) return "hidden";
  if (layout === "worker") return "visible";
  if (isLifecycleVisibleTool(toolName)) return "visible";
  if (!resultMsg) return "verbose_only";
  if (resultMsg.tool_result?.file_edit_preview?.path?.trim()) return "visible";
  return isBenignToolResult(resultMsg) ? "verbose_only" : "visible";
}

/** Quiet chat defers ordinary tools until they settle. */
export function shouldOmitToolPartFromTranscript(
  resultMsg: Message | undefined,
  toolName: string,
  options: ToolPartTranscriptOmitOptions,
): boolean {
  const layout = options.layout ?? "chat";
  const visibility = toolTranscriptVisibility(resultMsg, toolName, layout);
  if (visibility === "visible") return false;
  if (visibility === "hidden") return true;
  return !options.verboseMode;
}

/** One-line label for collapsed group hints and card summaries. */
export function toolPartSummaryTitle(part: ToolPartView): string {
  // The host-stamped skill identifies the activation reliably.
  const activated = part.skill?.name?.trim();
  const resource = skillReadResource(part);
  if (resource) {
    const requested = typeof part.args?.need === "string" ? part.args.need.trim() : "";
    const name = activated || requested;
    return name ? `${name} · ${skillResourceTitle(resource)}` : skillResourceTitle(resource);
  }
  if (activated) return activated;
  const titled = part.title?.trim();
  if (titled && titled !== part.tool) return titled;
  const fromArgs = resolveChickletTitle(part.tool, part.args);
  if (fromArgs && fromArgs !== part.tool) return fromArgs;
  return part.tool;
}

/** Resolves the message and field path carrying argument spans. */
function argsRedactionFor(
  resultMsg: Message | undefined,
  assistantCall: AssistantCallSite | undefined,
): ToolArgsRedaction | null {
  const fromResult = resultMsg?.host_secret_redaction;
  if (fromResult && resultMsg?.tool_result?.tool_args) {
    return { meta: fromResult, fieldPrefix: REDACTION_FIELD_TOOL_RESULT_ARGS };
  }
  const fromCall = assistantCall?.message.host_secret_redaction;
  if (fromCall) {
    return {
      meta: fromCall,
      fieldPrefix: toolCallArgsRedactionField(assistantCall.index),
    };
  }
  return null;
}

export type AssistantCallSite = { message: Message; index: number };

/** Builds a tool card from a call and optional result. */
export function toolPartFromCall(
  call: ToolCall,
  resultMsg: Message | undefined,
  assistantMessageId: string,
  workflowRunId?: string,
  assistantCall?: AssistantCallSite,
): ToolPartView {
  const output = toolOutputText(resultMsg);
  const status = statusFromToolResult(resultMsg, output);
  const jobId = resultMsg?.tool_result?.dispatch?.worker_id?.trim();
  return {
    id: toolPartStableId(assistantMessageId, call),
    toolCallId: call.id,
    assistantMessageId,
    workflowRunId:
      workflowRunId?.trim() || resultMsg?.workflow_run_id?.trim() || undefined,
    messageId: resultMsg?.id ?? assistantMessageId,
    tool: call.name,
    kind: classifyToolKind(call.name),
    status,
    title: resultMsg?.tool_result?.display_title ?? call.display_title ?? resolveChickletTitle(call.name, call.args),
    displaySubject: resultMsg?.tool_result?.display_subject,
    args: call.args,
    output,
    outputReference: resultMsg?.tool_result?.content_ref,
    argsReference: call.args_ref ?? resultMsg?.tool_result?.tool_args_ref,
    argsMessageId: call.args_ref ? assistantMessageId : resultMsg?.id,
    error: status === "error" ? output : null,
    jobId,
    childSessionId: resultMsg?.tool_result?.dispatch?.child_session_id?.trim(),
    fileEdit: resultMsg?.tool_result?.file_edit_preview ?? null,
    visual: resultMsg?.tool_result?.visual ?? null,
    externalAccess: resultMsg?.tool_result?.external_access ?? null,
    externallyAuthored: EXTERNALLY_AUTHORED_ORIGINS.has(resultMsg?.origin ?? ""),
    // Result-row provenance identifies host redaction.
    redaction: resultMsg?.host_secret_redaction ?? null,
    argsRedaction: argsRedactionFor(resultMsg, assistantCall),
    skill: resultMsg?.tool_result?.skill ?? null,
    workerSummary: resultMsg?.worker_summary,
    completion: resultMsg?.tool_result?.completion ?? null,
    process: resultMsg?.tool_result?.process ?? null,
    invocation: resultMsg?.tool_result?.invocation ?? null,
    verdict: resultMsg?.tool_result?.verdict ?? null,
    outcome: resultMsg?.tool_result?.outcome,
    codes: resultMsg?.tool_result?.codes,
    feedback: resultMsg?.tool_result?.feedback,
  };
}

export function toolResultText(part: ToolPartView): string | null {
  if (part.status === "error") {
    return part.error ?? part.output ?? null;
  }
  if (part.status === "completed") return part.output ?? null;
  // Yielded/background command: the tool returned a handle while still running.
  if (part.status === "running" && liveToolProcess(part)) {
    return part.output ?? null;
  }
  return null;
}

export function toolSummaryFields(
  part: ToolPartView,
  sessionId?: string,
): {
  status: ToolPartStatus;
  title: string;
} {
  return {
    status: toolPartDisplayStatus(part, sessionId),
    title: toolPartSummaryTitle(part),
  };
}

export function toolCompletionLabel(part: ToolPartView): string | undefined {
  const completion = part.completion;
  const operation = completion?.operation.trim().replaceAll("_", " ");
  const state = completion?.state.trim().replaceAll("_", " ");
  if (!operation || !state) return undefined;
  return `${operation} · ${state}`;
}

/** True when tool arguments target a local loopback service. */
export function isLoopbackTarget(args?: Record<string, unknown>): boolean {
  if (!args) return false;
  if (args.capability_request && typeof args.capability_request === "object") {
    const cap = args.capability_request as Record<string, unknown>;
    if (cap.loopback_connect) return true;
  }
  const rawUrl = typeof args.url === "string" ? args.url.trim() : "";
  if (!rawUrl) return false;
  try {
    const parsed = new URL(rawUrl);
    const host = parsed.hostname.toLowerCase();
    return (
      host === "localhost" ||
      host === "127.0.0.1" ||
      host === "::1" ||
      host === "[::1]" ||
      host.endsWith(".localhost") ||
      host.endsWith(".local") ||
      host.startsWith("127.")
    );
  } catch {
    const lower = rawUrl.toLowerCase();
    return (
      lower.includes("localhost") ||
      lower.includes("127.0.0.1") ||
      lower.includes("::1")
    );
  }
}

export function toolRunningLabel(part: ToolPartView): string {
  const tool = part.tool.toLowerCase();
  if (part.args?.background === true && part.kind === "command") {
    return "Background process…";
  }
  if (tool === "http_request" && isLoopbackTarget(part.args)) {
    return "Testing endpoint…";
  }
  const mapped = TOOL_CHICKLET_RUNNING_LABELS[tool];
  if (mapped) return mapped;
  if (part.kind === "read") return "Reading…";
  if (part.kind === "write") return "Writing…";
  return "Running…";
}
