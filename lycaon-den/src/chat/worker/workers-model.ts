import type {
  WorkerContextUsage,
  WorkerTask,
  WorkerEvent,
} from "../../api/types.ts";
import type { Message } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import {
  taskJobIdFromPart,
  taskJobIdFromToolMessage,
  isEnqueuedWorkerDispatchResult,
} from "../task/task-result-model.ts";
import type { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import { isTaskToolName } from "../tool/tool-part-model.ts";
import { workerOverlayOpen } from "./worker-branch-model.ts";
import { workerFailureFromDispatchReject } from "./worker-failure-model.ts";

/** Prefix for rejected dispatches without job IDs. */
const FAILED_DISPATCH_WORKER_ID_PREFIX = "dispatch-reject:";

export function failedDispatchWorkerId(toolCallId: string): string {
  return `${FAILED_DISPATCH_WORKER_ID_PREFIX}${toolCallId.trim()}`;
}

function toolCallIdFromFailedDispatchWorkerId(
  id: string,
): string | undefined {
  if (!id.startsWith(FAILED_DISPATCH_WORKER_ID_PREFIX)) return undefined;
  const callId = id.slice(FAILED_DISPATCH_WORKER_ID_PREFIX.length).trim();
  return callId || undefined;
}

export type WorkerRowStatus =
  | "running"
  | "needs_decision"
  | "done"
  | "partial"
  | "open"
  | "canceled"
  | "error";

/** Optional focus when opening the workers drawer from a transcript card. */
export type OpenWorkerOptions = {
  scrollTo?: "evidence";
};

export type WorkerDrawerFocus = "evidence" | null;

function workerResultSummaryStatus(worker: WorkerTask): string | undefined {
  return worker.result?.status?.trim().toLowerCase();
}

/** Terminal row status from queue job + optional result/summary tag (not tool rejects). */
export function workerRowStatus(worker: WorkerTask): WorkerRowStatus {
  switch (worker.status) {
    case "running":
    case "pending":
    case "held":
      return "running";
    case "complete": {
      if (worker.merge_status === "merged" || worker.merge_status === "rejected") {
        return "done";
      }
      if (worker.merge_status === "orphaned") return "partial";
      if (workerResultSummaryStatus(worker) === "needs_decision") {
        return "needs_decision";
      }
      if (workerResultSummaryStatus(worker) === "partial") return "partial";
      if (workerOverlayOpen(worker) || workerResultSummaryStatus(worker) === "open") {
        return "open";
      }
      return "done";
    }
    case "failed":
      return "error";
    case "canceled":
      return "canceled";
    default:
      return "running";
  }
}

export function workerAgentLabel(worker: WorkerTask): string {
  return worker.agent_type?.trim() || "worker";
}

/** First non-empty line of text, for card titles and drawer rows. */
export function workerBriefTitleLine(
  brief: string | undefined,
): string | undefined {
  const text = brief?.trim() ?? "";
  if (!text) return undefined;
  return text.split("\n").find((line) => line.trim())?.trim() || undefined;
}

export function workerRowTitle(worker: WorkerTask): string {
  return workerBriefTitleLine(worker.brief) ?? workerAgentLabel(worker);
}

/** Disambiguate duplicate briefs in the workers drawer. */
export function workerRowTitleDisambiguated(
  worker: WorkerTask,
  peers: readonly WorkerTask[],
): string {
  const title = workerRowTitle(worker);
  const dupes = peers.filter((w) => workerRowTitle(w) === title);
  if (dupes.length <= 1) return title;
  const id = worker.id?.trim();
  if (id && id.length >= 8) return `${title} · ${id.slice(0, 8)}`;
  return title;
}

/** Stable comparison for store writes — skip when SSE/HTTP payloads are unchanged. */
export function workerStoreFingerprint(worker: WorkerTask): string {
  return [
    worker.id,
    worker.status,
    worker.agent_type ?? "",
    worker.brief ?? "",
    worker.leg_id ?? "",
    JSON.stringify(worker.dependencies ?? []),
    worker.merge_status ?? "",
    worker.child_session_id ?? "",
    worker.parent_session_id ?? "",
    worker.result?.status ?? "",
    worker.failure?.code ?? "",
    worker.failure?.message ?? "",
    worker.error ?? "",
    worker.created_at ?? "",
    String(worker.max_tool_loops ?? ""),
    String(worker.budget_request?.requested_max ?? ""),
    String(worker.tool_loops_used ?? ""),
    String(worker.tool_calls_used ?? ""),
    String(worker.turn_tool_calls ?? ""),
    String(worker.turn_tools_done ?? ""),
    String(worker.context_usage?.prompt_tokens ?? ""),
    String(worker.context_usage?.window ?? ""),
    String(worker.context_usage?.compaction_threshold ?? ""),
    worker.workspace_preparation?.strategy ?? "",
    worker.workspace_preparation?.stage ?? "",
    String(worker.workspace_preparation?.files ?? ""),
    String(worker.workspace_preparation?.bytes ?? ""),
    String(worker.workspace_preparation?.total_bytes ?? ""),
  ].join("\0");
}

export function workerTasksSliceEqual(
  a: readonly WorkerTask[],
  b: readonly WorkerTask[],
): boolean {
  if (a.length !== b.length) return false;
  const sortedA = [...a].sort((x, y) => x.id.localeCompare(y.id));
  const sortedB = [...b].sort((x, y) => x.id.localeCompare(y.id));
  for (let i = 0; i < sortedA.length; i++) {
    if (
      workerStoreFingerprint(sortedA[i]!) !==
      workerStoreFingerprint(sortedB[i]!)
    ) {
      return false;
    }
  }
  return true;
}

function preferWorkerContextUsage(
  existing: WorkerContextUsage | undefined,
  incoming: WorkerContextUsage | undefined,
): WorkerContextUsage | undefined {
  if (!existing) return incoming;
  if (!incoming) return existing;
  const existingTokens = existing.prompt_tokens ?? 0;
  const incomingTokens = incoming.prompt_tokens ?? 0;
  return incomingTokens >= existingTokens ? incoming : existing;
}

/** The progress slice of a worker row: budget, lifetime counters, in-flight batch. */
export type WorkerProgressFields = {
  max_tool_loops?: number;
  tool_loops_used?: number;
  tool_calls_used?: number;
  turn_tool_calls?: number;
  turn_tools_done?: number;
};

function monotonic(
  existing: number | undefined,
  incoming: number | undefined,
): number | undefined {
  if (existing == null && incoming == null) return undefined;
  return Math.max(existing ?? 0, incoming ?? 0);
}

/** Preserve monotonic progress under out-of-order payloads. */
function mergedLifetimeCounters(
  existing: WorkerProgressFields | undefined,
  incoming: WorkerProgressFields,
): WorkerProgressFields {
  return {
    max_tool_loops: incoming.max_tool_loops ?? existing?.max_tool_loops,
    tool_loops_used: monotonic(existing?.tool_loops_used, incoming.tool_loops_used),
    tool_calls_used: monotonic(existing?.tool_calls_used, incoming.tool_calls_used),
  };
}

/** Merge an event that replaces in-flight batch progress. */
export function mergedWorkerProgressFromEvent(
  existing: WorkerProgressFields | undefined,
  incoming: WorkerProgressFields,
): WorkerProgressFields {
  return {
    ...mergedLifetimeCounters(existing, incoming),
    turn_tool_calls: incoming.turn_tool_calls,
    turn_tools_done: incoming.turn_tools_done,
  };
}

/** Merge a list checkpoint while retaining live batch progress. */
export function mergedWorkerProgressFromListRow(
  existing: WorkerProgressFields | undefined,
  incoming: WorkerProgressFields,
): WorkerProgressFields {
  return {
    ...mergedLifetimeCounters(existing, incoming),
    turn_tool_calls: incoming.turn_tool_calls ?? existing?.turn_tool_calls,
    turn_tools_done: incoming.turn_tools_done ?? existing?.turn_tools_done,
  };
}

/** Merge a listWorkers row onto cache; keep the higher live progress counters. */
export function reconcileWorkerFromListRefresh(
  existing: WorkerTask | undefined,
  incoming: WorkerTask,
): WorkerTask {
  if (!existing) return incoming;
  return {
    ...incoming,
    ...mergedWorkerProgressFromListRow(existing, incoming),
    context_usage: preferWorkerContextUsage(
      existing.context_usage,
      incoming.context_usage,
    ),
  };
}

/** Session-scoped list refresh merged per job id against the cached slice. */
export function reconcileSessionWorkersFromList(
  existingSlice: readonly WorkerTask[],
  incoming: readonly WorkerTask[],
): WorkerTask[] {
  const existingById = new Map(existingSlice.map((w) => [w.id, w]));
  return incoming.map((row) =>
    reconcileWorkerFromListRefresh(existingById.get(row.id), row),
  );
}

/** Keep one worker row per job ID. */
export function dedupeWorkersById(workers: readonly WorkerTask[]): WorkerTask[] {
  const byId = new Map<string, WorkerTask>();
  for (const w of workers) {
    const id = w.id?.trim();
    if (!id) continue;
    const prev = byId.get(id);
    if (!prev) {
      byId.set(id, w);
      continue;
    }
    const prevTs = Date.parse(prev.created_at);
    const nextTs = Date.parse(w.created_at);
    if (Number.isNaN(prevTs) || (!Number.isNaN(nextTs) && nextTs >= prevTs)) {
      byId.set(id, w);
    }
  }
  return [...byId.values()];
}

/** Running workers first, then newest first. */
export function sortWorkerTasks(workers: WorkerTask[]): WorkerTask[] {
  return dedupeWorkersById(workers).sort((a, b) => {
    const aRun = workerRowStatus(a) === "running" ? 0 : 1;
    const bRun = workerRowStatus(b) === "running" ? 0 : 1;
    if (aRun !== bRun) return aRun - bRun;
    return Date.parse(b.created_at) - Date.parse(a.created_at);
  });
}

/** Workers belonging to the active coordinator session. */
export function workersForSession(
  workers: readonly WorkerTask[],
  sessionId: string,
): WorkerTask[] {
  const sid = sessionId.trim();
  if (!sid) return [...workers];
  return workers.filter((w) => w.parent_session_id?.trim() === sid);
}


function taskEnqueueAgentType(
  messages: readonly Message[],
  toolMessage: Message,
): string | undefined {
  const call = taskDispatchCallForToolMessage(messages, toolMessage);
  if (!call) return undefined;
  return dispatchAgentTypeFromArgs(call.args) || undefined;
}

export type WorkerTaskDispatchCall = {
  tool: string;
  args: Record<string, unknown>;
};

const DISPATCH_PARAM_SKIP = new Set([
  "brief",
  "job_id",
  "child_session_id",
]);

const DISPATCH_PARAM_ORDER = [
  "agent_type",
  "subagent_type",
  "scope",
  "files",
  "max_tool_loops",
] as const;

const DISPATCH_PARAM_LABELS: Record<string, string> = {
  agent_type: "Agent",
  subagent_type: "Agent",
  scope: "Scope",
  files: "Files",
  max_tool_loops: "Max tool loops",
};

function dispatchBriefFromArgs(args?: Record<string, unknown>): string {
  if (!args) return "";
  const brief = args.brief;
  if (!brief || typeof brief !== "object" || Array.isArray(brief)) return "";
  const goal = (brief as { goal?: unknown }).goal;
  return typeof goal === "string" ? goal.trim() : "";
}

function dispatchAgentTypeFromArgs(args?: Record<string, unknown>): string {
  if (!args) return "";
  if (typeof args.agent_type === "string" && args.agent_type.trim()) {
    return args.agent_type.trim();
  }
  if (typeof args.subagent_type === "string" && args.subagent_type.trim()) {
    return args.subagent_type.trim();
  }
  return "";
}

function dispatchMaxToolLoops(args?: Record<string, unknown>): number | undefined {
  const raw = args?.max_tool_loops;
  if (typeof raw === "number" && raw > 0) return raw;
  return undefined;
}

function formatDispatchParamValue(key: string, value: unknown): string | null {
  if (value === null || value === undefined) return null;
  if (key === "scope" && typeof value === "object" && !Array.isArray(value)) {
    const scope = value as { mode?: string; paths?: string[] };
    const mode = scope.mode?.trim();
    const paths = (scope.paths ?? []).map((p) => p.trim()).filter(Boolean);
    if (mode && paths.length) return `${mode} · focus: ${paths.join(", ")}`;
    if (mode) return mode;
    if (paths.length) return `focus: ${paths.join(", ")}`;
    return null;
  }
  if (Array.isArray(value)) {
    const items = value.map((item) => String(item).trim()).filter(Boolean);
    return items.length ? items.join(", ") : null;
  }
  if (typeof value === "string") return value.trim() || null;
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  if (typeof value === "object") {
    try {
      return JSON.stringify(value);
    } catch {
      return null;
    }
  }
  return null;
}

/** Walk tool results carrying their immutable call snapshot. */
function forEachStructuredToolResult(
  messages: readonly Message[],
  visit: (pair: {
    call: NonNullable<Message["tool_calls"]>[number];
    result: Message;
  }) => boolean | void,
): void {
  for (const result of messages) {
    if (result.role !== "tool") continue;
    const tool = result.tool_result?.tool?.trim();
    const id = result.tool_result?.tool_call_id?.trim();
    const assistantMessageId =
      result.tool_result?.assistant_message_id?.trim();
    if (!tool || !id || !assistantMessageId) continue;
    const call = { id, name: tool, args: result.tool_result?.tool_args ?? {} };
    if (visit({ call, result })) return;
  }
}

/** Parent transcript task()/delegate_dispatch call that enqueued this worker job. */
export function taskDispatchCallForWorker(
  messages: readonly Message[],
  workerId: string,
): WorkerTaskDispatchCall | null {
  const id = workerId.trim();
  if (!id) return null;
  const rejectCallId = toolCallIdFromFailedDispatchWorkerId(id);
  let matched: WorkerTaskDispatchCall | null = null;
  forEachStructuredToolResult(messages, ({ call, result }) => {
    if (!call.name?.trim() || !isTaskToolName(call.name)) return;
    if (rejectCallId && call.id?.trim() === rejectCallId) {
      matched = { tool: call.name, args: call.args ?? {} };
      return true;
    }
    const jobId = taskJobIdFromToolMessage(result);
    if (jobId === id) {
      matched = { tool: call.name, args: call.args ?? {} };
      return true;
    }
  });
  return matched;
}

function taskDispatchCallForToolMessage(
  messages: readonly Message[],
  toolMessage: Message,
): WorkerTaskDispatchCall | null {
  if (toolMessage.role !== "tool") return null;
  const jobId = taskJobIdFromToolMessage(toolMessage);
  if (!jobId) return null;
  return taskDispatchCallForWorker(messages, jobId);
}

/** Worker assignment title for the panel. */
export function workerDispatchBrief(worker: WorkerTask): string {
  return worker.brief?.trim() ?? "";
}

export type WorkerDispatchParamRow = { label: string; value: string };

/** Labeled dispatch args for the worker panel. */
export function workerDispatchParamRows(
  worker: WorkerTask,
  messages: readonly Message[],
): WorkerDispatchParamRow[] {
  const call = taskDispatchCallForWorker(messages, worker.id);
  if (!call) return [];
  const rows: WorkerDispatchParamRow[] = [];
  const seen = new Set<string>();
  const push = (key: string) => {
    if (DISPATCH_PARAM_SKIP.has(key) || seen.has(key)) return;
    const value = formatDispatchParamValue(key, call.args[key]);
    if (!value) return;
    seen.add(key);
    rows.push({
      label: DISPATCH_PARAM_LABELS[key] ?? key.replace(/_/g, " "),
      value,
    });
  };
  for (const key of DISPATCH_PARAM_ORDER) push(key);
  for (const key of Object.keys(call.args).sort()) push(key);
  return rows;
}

/** Fill parent_session_id when worker SSE omits it on the active session. */
export function normalizeWorkerEvent(
  event: WorkerEvent,
  activeSessionId?: string | null,
): WorkerEvent {
  if (event.parent_session_id?.trim()) return event;
  const parentId = activeSessionId?.trim();
  if (!parentId) return event;
  return { ...event, parent_session_id: parentId };
}

/** Build a worker SSE-shaped row from an authoritative task() tool result. */
export function workerEventFromTaskToolMessage(
  sessionId: string,
  messages: readonly Message[],
  toolMessage: Message,
): WorkerEvent | null {
  if (toolMessage.role !== "tool") return null;
  const jobId = taskJobIdFromToolMessage(toolMessage);
  if (!jobId) return null;
  const call = taskDispatchCallForWorker(messages, jobId);
  const brief = dispatchBriefFromArgs(call?.args);
  const maxToolLoops = dispatchMaxToolLoops(call?.args);
  const agentFromArgs = dispatchAgentTypeFromArgs(call?.args);
  return {
    worker_id: jobId,
    status: "pending",
    parent_session_id: sessionId,
    child_session_id:
      toolMessage.tool_result?.dispatch?.child_session_id?.trim() || undefined,
    agent_type:
      agentFromArgs ||
      taskEnqueueAgentType(messages, toolMessage) ||
      undefined,
    brief: brief || undefined,
    max_tool_loops: maxToolLoops,
  };
}

/** Seed worker roster rows from an authoritative parent transcript snapshot. */
export function syncWorkersFromSessionTranscript(
  appStore: AppStore,
  sessionId: string,
  messages: readonly Message[],
): void {
  for (const msg of messages) {
    if (msg.role !== "tool") continue;
    syncWorkerFromTaskToolMessage(appStore, sessionId, msg);
  }
}

/** Dispatch results populate the roster before worker events arrive. */
export function syncWorkerFromTaskToolMessage(
  appStore: AppStore,
  sessionId: string,
  toolMessage: Message,
): void {
  const ev = workerEventFromTaskToolMessage(
    sessionId,
    appStore.state.messages,
    toolMessage,
  );
  if (!ev) return;
  const existing = appStore.state.workers.find((w) => w.id === ev.worker_id);
  if (!existing) {
    appStore.actions.updateWorker(ev);
    return;
  }
  const parentSessionId =
    existing.parent_session_id?.trim() || ev.parent_session_id;
  const childSessionId =
    existing.child_session_id?.trim() || ev.child_session_id;
  const agentType = existing.agent_type?.trim() || ev.agent_type;
  const brief = existing.brief?.trim() || ev.brief?.trim();
  const maxToolLoops = existing.max_tool_loops ?? ev.max_tool_loops;
  if (
    parentSessionId === existing.parent_session_id?.trim() &&
    childSessionId === existing.child_session_id?.trim() &&
    agentType === existing.agent_type?.trim() &&
    brief === existing.brief?.trim() &&
    maxToolLoops === existing.max_tool_loops
  ) {
    return;
  }
  appStore.actions.updateWorker({
    worker_id: ev.worker_id,
    status: existing.status,
    parent_session_id: parentSessionId,
    child_session_id: childSessionId,
    agent_type: agentType,
    brief,
    max_tool_loops: maxToolLoops,
    merge_status: existing.merge_status,
    result: existing.result,
    failure: existing.failure,
    error: existing.error,
  });
}

export type MatchWorkerForTaskOptions = {
  sessionId?: string;
  /** Job ids already bound to an earlier task card in the transcript. */
  excludeIds?: ReadonlySet<string>;
};

export function matchWorkerForTask(
  part: ToolPartView,
  workers: WorkerTask[],
  options?: MatchWorkerForTaskOptions,
): WorkerTask | undefined {
  let pool = workers;
  const sessionId = options?.sessionId?.trim();
  if (sessionId) {
    pool = workersForSession(pool, sessionId);
  }
  if (pool.length === 0) return undefined;

  const exclude = options?.excludeIds ?? new Set<string>();
  const available = pool.filter((w) => !exclude.has(w.id));

  const jobId = taskJobIdFromPart(part);
  if (jobId) {
    const byJob = available.find((w) => w.id === jobId);
    if (byJob) return byJob;
  }
  const childSessionId = part.childSessionId?.trim();
  if (childSessionId) {
    const byChild = available.find(
      (w) => w.child_session_id?.trim() === childSessionId,
    );
    if (byChild) return byChild;
  }
  const callId = part.toolCallId?.trim();
  if (callId) {
    const rejectId = failedDispatchWorkerId(callId);
    const byReject = available.find((w) => w.id === rejectId);
    if (byReject) return byReject;
  }

  return undefined;
}

/** Bind task tool cards to distinct workers (newest-first when job_id is absent). */
export function taskWorkerMatchesForTranscript(
  items: readonly TranscriptItem[],
  workers: WorkerTask[],
  sessionId: string,
): ReadonlyMap<string, WorkerTask> {
	const matches = new Map<string, WorkerTask>();
	const assigned = new Set<string>();
	for (const item of items) {
		const rows =
			item.kind === "tool"
				? [{ key: item.key, part: item.part }]
				: item.kind === "worker_group"
					? item.parts.map((part) => ({ key: part.id, part }))
				: item.kind === "activity_span"
					? item.entries.flatMap((entry) =>
							entry.kind === "tool"
								? [{ key: entry.part.id, part: entry.part }]
								: [],
						)
					: [];
		for (const { key, part } of rows) {
			if (!isTaskToolName(part.tool)) continue;
			const worker = matchWorkerForTask(part, workers, {
				sessionId,
				excludeIds: assigned,
			});
			if (!worker) continue;
			matches.set(key, worker);
			assigned.add(worker.id);
		}
	}
  return matches;
}

export function canCancelWorker(worker: WorkerTask): boolean {
  return worker.status === "running" || worker.status === "pending" || worker.status === "waiting" || worker.status === "held";
}

/** Active workers and completed workers with unmerged overlays. */
export function liveWorkerRows(workers: readonly WorkerTask[]): WorkerTask[] {
  return sortWorkerTasks([...workers]).filter((w) => {
    const status = workerRowStatus(w);
    return status === "running" || status === "open";
  });
}

/** Failed and canceled workers appear only in verbose mode. */
export function sessionWorkerRows(
  workers: readonly WorkerTask[],
  opts?: { verboseMode?: boolean },
): WorkerTask[] {
  const verboseMode = opts?.verboseMode ?? false;
  return sortWorkerTasks([...workers]).filter((w) => {
    if (w.status === "failed" || w.status === "canceled") return verboseMode;
    return true;
  });
}

/** Derive failed dispatch rows without a job ID. */
export function failedDispatchWorkersFromMessages(
  sessionId: string,
  messages: readonly Message[],
): WorkerTask[] {
  const sid = sessionId.trim();
  if (!sid) return [];
  const rows: WorkerTask[] = [];
  forEachStructuredToolResult(messages, ({ call, result }) => {
    const callId = call.id?.trim();
    const toolName = call.name?.trim() ?? "";
    if (!callId || !isTaskToolName(toolName)) return;
    const outcome = result.tool_result?.outcome;
    if (outcome !== "rejected" && outcome !== "error") return;
    if (isEnqueuedWorkerDispatchResult(toolName, result)) return;
    const args = call.args ?? {};
    const brief = dispatchBriefFromArgs(args);
    const failure = workerFailureFromDispatchReject(
      result.tool_result?.feedback?.[0]?.code,
    );
    rows.push({
      id: failedDispatchWorkerId(callId),
      parent_session_id: sid,
      agent_type: dispatchAgentTypeFromArgs(args) || "worker",
      status: "failed",
      brief: brief || undefined,
      failure,
      error: failure.message,
      created_at:
        result.created_at?.trim() || new Date(0).toISOString(),
    });
  });
  return rows;
}

export function workerDependencyLabel(worker: WorkerTask): string | undefined {
  if (worker.status !== "pending") return undefined;
  const blocked = worker.dependencies?.filter((item) => item.state === "blocked").length ?? 0;
  if (blocked > 0) return blocked === 1 ? "1 producer needs attention" : `${blocked} producers need attention`;
  const waiting = worker.dependencies?.filter((item) => item.state === "waiting").length ?? 0;
  if (waiting > 0) return waiting === 1 ? "Waiting for 1 producer" : `Waiting for ${waiting} producers`;
  return undefined;
}
