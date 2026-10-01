import type {
  ActivityEvent,
  CoordinatorBatchPhase,
  CoordinatorLoopProgress,
  Message,
  ProgressStep,
  ProviderKindTemplate,
  ProviderMeta,
  TurnLoadTrigger,
  WorkerTask,
  WorkflowRun,
} from "../../api/types.ts";
import { providerLabelForId } from "../../settings/providers/models-editor-model.ts";
import type { LlmTurnActivity } from "../../store/app-state-model.ts";
import { resolveChickletTitle } from "../tool/tool-chicklet-titles.ts";
import { TOOL_CHICKLET_RUNNING_LABELS } from "../tool/tool-presentation.generated.ts";
import {
  classifyToolKind,
  isCoordinatorInternalTool,
  isLoopbackTarget,
  toolPartFromCall,
  type ToolPartKind,
  type ToolPartView,
} from "../tool/tool-part-model.ts";
import {
  findToolResult,
  isInternalTranscriptUserMessage,
} from "../transcript/projection/message-transcript.ts";
import { isWorkerTaskActive } from "./session-activity.ts";
import {
  workerBriefTitleLine,
} from "../worker/workers-model.ts";
import {
  liveProgressItems,
  visibleBatchPhase,
  type VisibleBatchPhase,
} from "../progress/progress-model.ts";
import { BATCH_PHASE_LABELS } from "../progress/batch-phase-copy.generated.ts";
import { workflowObligationSuffix, workflowPhaseLabel, workflowTopologySuffix } from "../../workflow/workflow-phase-labels.ts";

/** Signal source breaks equal-weight ties. */
export type ActivityLabelSource =
  | "provider_wait"
  | "host_activity"
  | "running_tool"
  | "provisional_closeout"
  | "progress_step"
  | "batch_phase"
  | "workflow_phase"
  | "coordinator_surface"
  | "worker"
  | "prompt_starting"
  | "default";

export type ActivityLabelCandidate = {
  label: string;
  weight: number;
  source: ActivityLabelSource;
};

/** Relative activity priority. Higher values win. */
export const ACTIVITY_LABEL_WEIGHTS = {
  providerWait: 120,
  hostRunningTool: 105,
  deciding: 105,
  runningTool: 100,
  preparingContext: 90,
  startingWorkflow: 90,
  humanReview: 80,
  provisionalCloseout: 72,
  workerRich: 60,
  batchPhase: 48,
  // Richer worker or batch state outranks an armed sleep.
  awaitingWake: 30,
  // Unnamed waits rank below workflow phases.
  awaitingWakeUnnamed: 16,
  coordinatorSurfaceHost: 36,
  coordinatorSurfaceIdle: 26,
  workflowPhase: 20,
  promptStarting: 18,
  // Checklist rows describe planned activity.
  progressStepFirstOpen: 14,
  default: 10,
} as const;

/** When the runner-up is within this gap, batch phase may prefix the winner. */
const BATCH_PHASE_COMBINE_GAP = 12;

const WAIT_CONDITION_PRIORITY = [
  "process_done",
  "http_ready",
  "port_ready",
  "overlay_promote_pending",
  "scan_done",
  "all_workers_idle",
  "next_worker_done",
] as const;

const WAIT_CONDITION_LABELS: Record<(typeof WAIT_CONDITION_PRIORITY)[number] | "timer", string> = {
  process_done: "Waiting for a command to finish",
  http_ready: "Waiting for a service",
  port_ready: "Waiting for a port",
  next_worker_done: "Waiting for workers",
  all_workers_idle: "Waiting for workers",
  scan_done: "Waiting for search to finish",
  overlay_promote_pending: "Waiting to combine results",
  timer: "Waiting",
};

const THINKING_FALLBACK_LABEL = "Thinking";
const THINKING_SENDING_LABEL = "Sending your message";
const THINKING_DRAFTING_REPORT_LABEL = "Putting it together";
const THINKING_BACKGROUND_COMMAND_LABEL = "Running a background command";
const THINKING_GENERIC_TOOL_LABEL = "Using a tool";
const THINKING_PROVIDER_WAIT_VERB = "Waiting on";
const THINKING_RETRY_VERB = "retrying";
const THINKING_BUSY_VERB = "still busy";
const THINKING_WENT_QUIET_VERB = "went quiet";
const THINKING_NO_ANSWER_VERB = "no answer";

/** Maximum composed activity label length. */
const THINKING_LABEL_MAX = 72;

/** Default activity verbs by tool kind. */
const KIND_RUNNING_VERBS: Partial<
  Record<ToolPartKind, { withObject: string; alone: string }>
> = {
  read: { withObject: "Reading", alone: "Reading files" },
  write: { withObject: "Writing", alone: "Writing files" },
};

/** Report activity only for workflow states that can be running. */
const ACTIVE_WORKFLOW_RUN_STATUSES = new Set<WorkflowRun["status"]>([
  "running",
  "paused_on_child",
]);

export type ThinkingActivityInput = {
  sessionId?: string;
  messages?: readonly Message[];
  activities?: readonly ActivityEvent[];
  coordinatorLoop?: CoordinatorLoopProgress | null;
  progressSteps?: readonly ProgressStep[];
  progressRunning?: boolean;
  batchPhase?: CoordinatorBatchPhase;
  activeWorkflowRun?: WorkflowRun | null;
  workers?: readonly WorkerTask[];
  llmTurnActivity?: LlmTurnActivity;
  /** A prompt waits to start while no turn runs. */
  promptStarting?: boolean;
  /** Provider catalog, for naming the provider a live call is waiting on. */
  providers?: readonly ProviderMeta[];
  /** Provider kinds — the canonical name behind an instance id. */
  providerKinds?: readonly ProviderKindTemplate[];
};

/** Returns the generated label for a batch phase. */
export function batchPhaseActivityLabel(phase: VisibleBatchPhase): string {
  return BATCH_PHASE_LABELS[phase];
}

/** Catalog ellipses are decorative, not truncation markers. */
function packRunningVerb(label: string): string {
  return label.trim().replace(/…+$/u, "").trim();
}

/** Truncated labels retain `…`; complete labels end with `...`. */
export function formatThinkingLabel(label: string): string {
  const text = label.replace(/\s+/gu, " ").trim() || THINKING_FALLBACK_LABEL;
  // Code-point slicing preserves surrogate pairs.
  const chars = Array.from(text);
  if (chars.length > THINKING_LABEL_MAX) {
    return `${chars.slice(0, THINKING_LABEL_MAX - 1).join("").trimEnd()}…`;
  }
  if (text.endsWith("…")) return text;
  return `${text.replace(/\.+$/u, "").trimEnd()}...`;
}

/** User-facing label for a running wait() call from structured args. */
export function waitToolRunningLabel(
  args?: Record<string, unknown>,
): string {
  const conditionsRaw = args?.conditions;
  const conditions = Array.isArray(conditionsRaw)
    ? conditionsRaw.flatMap((entry) => {
        if (!entry || typeof entry !== "object") return [];
        const kind = (entry as { kind?: unknown }).kind;
        return typeof kind === "string" ? [kind] : [];
      })
    : [];
  const subscribed = waitConditionsLabel(conditions);
  if (subscribed) return subscribed;
  const reason = typeof args?.reason === "string" ? args.reason.trim() : "";
  if (reason) return reason;
  return WAIT_CONDITION_LABELS.timer;
}

/** Tool arguments and host leases share the same trigger priority. */
function waitConditionsLabel(triggers: readonly string[]): string | null {
  for (const trigger of WAIT_CONDITION_PRIORITY) {
    if (triggers.includes(trigger)) {
      return WAIT_CONDITION_LABELS[trigger];
    }
  }
  return null;
}

function toolRunningVerb(part: ToolPartView, hasObject: boolean): string {
  if (part.args?.background === true && part.kind === "command") {
    return THINKING_BACKGROUND_COMMAND_LABEL;
  }
  const tool = part.tool.toLowerCase();
  if (tool === "http_request" && isLoopbackTarget(part.args)) {
    return "Testing endpoint";
  }
  const mapped = TOOL_CHICKLET_RUNNING_LABELS[tool];
  if (mapped) return packRunningVerb(mapped);
  const verbs = KIND_RUNNING_VERBS[part.kind];
  if (!verbs) return THINKING_GENERIC_TOOL_LABEL;
  return hasObject ? verbs.withObject : verbs.alone;
}

function thinkingToolRunningLabel(part: ToolPartView): string {
  if (part.tool.toLowerCase() === "wait") {
    return waitToolRunningLabel(part.args);
  }
  const object = part.title || resolveChickletTitle(part.tool, part.args);
  const verb = toolRunningVerb(part, !!object);
  if (!object) return verb;
  // formatThinkingLabel applies the shared length limit.
  return `${verb} · ${object}`;
}

export function richWorkerActivityLabel(
  workers: readonly WorkerTask[],
): string | null {
  const active = workers.filter((w) => isWorkerTaskActive(w.status));
  const [newest] = active.sort(compareWorkerRecency);
  if (!newest) return null;
  const brief = workerBriefTitleLine(newest.brief);

  if (active.length === 1) {
    return brief || "Running a worker";
  }
  const count = `Running ${active.length} workers`;
  return brief ? `${count} · ${brief}` : count;
}

/** Most recently started first; id breaks ties so the pick is stable. */
function compareWorkerRecency(a: WorkerTask, b: WorkerTask): number {
  const at = Date.parse(a.started_at ?? a.created_at);
  const bt = Date.parse(b.started_at ?? b.created_at);
  const aMs = Number.isNaN(at) ? 0 : at;
  const bMs = Number.isNaN(bt) ? 0 : bt;
  if (aMs !== bMs) return bMs - aMs;
  return a.id.localeCompare(b.id);
}

function userTurnStartIndex(messages: readonly Message[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (msg?.role === "user" && !isInternalTranscriptUserMessage(msg)) {
      return i;
    }
  }
  return 0;
}

/** Latest in-flight tool in the current user turn (skips orientation-only tools). */
export function findLatestRunningToolInTurn(
  messages: readonly Message[],
): ToolPartView | undefined {
  const turnStart = userTurnStartIndex(messages);
  let latest: ToolPartView | undefined;

  for (let i = turnStart; i < messages.length; i++) {
    const msg = messages[i];
    if (msg?.role !== "assistant" || !msg.tool_calls?.length) continue;

    for (const [callIndex, call] of msg.tool_calls.entries()) {
      const toolName = call.name?.trim();
      if (!toolName) continue;
      if (isCoordinatorInternalTool(toolName)) continue;

      const result = findToolResult(messages, msg.id, call.id);
      const part = toolPartFromCall(call, result, msg.id, undefined, {
        message: msg,
        index: callIndex,
      });
      if (part.status === "running") {
        latest = part;
      }
    }
  }

  return latest;
}

function runningToolCandidate(
  messages: readonly Message[],
): ActivityLabelCandidate | undefined {
  const part = findLatestRunningToolInTurn(messages);
  if (!part) return undefined;
  return {
    label: thinkingToolRunningLabel(part),
    weight: ACTIVITY_LABEL_WEIGHTS.runningTool,
    source: "running_tool",
  };
}

function toolPartForActivity(
  activity: ActivityEvent,
  messages: readonly Message[],
): ToolPartView | undefined {
  const callID = activity.tool_call_id?.trim();
  const toolName = activity.tool_name?.trim();
  if (!callID || !toolName) return undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (msg?.role !== "assistant") continue;
    const index = msg.tool_calls?.findIndex(
      (candidate) =>
        candidate.id.trim() === callID &&
        candidate.name.trim().toLowerCase() === toolName.toLowerCase(),
    ) ?? -1;
    const call = index >= 0 ? msg.tool_calls?.[index] : undefined;
    if (call) {
      return toolPartFromCall(call, undefined, msg.id, undefined, {
        message: msg,
        index,
      });
    }
  }
  return undefined;
}

function newestActivity(
  activities: readonly ActivityEvent[],
): ActivityEvent | undefined {
  return [...activities].sort((a, b) => {
    const aTime = Date.parse(a.started_at);
    const bTime = Date.parse(b.started_at);
    const safeA = Number.isNaN(aTime) ? 0 : aTime;
    const safeB = Number.isNaN(bTime) ? 0 : bTime;
    if (safeA !== safeB) return safeB - safeA;
    return a.activity_id.localeCompare(b.activity_id);
  })[0];
}

/** What the local decision model is doing, by what asked it. */
const DECIDING_LABELS: Record<TurnLoadTrigger, string> = {
  turn: "Local AI is deciding what to load",
  request: "Local AI is matching tools",
  lookup: "Local AI is matching skills",
  tool_event: "Local AI is choosing a skill for this tool",
};

function decidingLabel(activity: ActivityEvent): string {
  return DECIDING_LABELS[activity.decision_trigger ?? "turn"];
}

/** Highest-confidence host activity: an open lease from the execution boundary. */
function hostActivityCandidate(
  activities: readonly ActivityEvent[] | undefined,
  messages: readonly Message[],
): ActivityLabelCandidate | undefined {
  const activity = newestActivity(
    (activities ?? []).filter((candidate) => candidate.status === "active"),
  );
  if (!activity) return undefined;
  if (activity.kind === "preparing_context") {
    return {
      label: "Preparing context",
      weight: ACTIVITY_LABEL_WEIGHTS.preparingContext,
      source: "host_activity",
    };
  }
  if (activity.kind === "deciding") {
    return {
      label: decidingLabel(activity),
      weight: ACTIVITY_LABEL_WEIGHTS.deciding,
      source: "host_activity",
    };
  }
  if (activity.kind === "starting_workflow") {
    return {
      label: "Starting workflow",
      weight: ACTIVITY_LABEL_WEIGHTS.startingWorkflow,
      source: "host_activity",
    };
  }
  if (activity.kind === "awaiting_wake") {
    const named = waitConditionsLabel(activity.wait_triggers ?? []);
    return {
      label: named ?? WAIT_CONDITION_LABELS.timer,
      weight: named
        ? ACTIVITY_LABEL_WEIGHTS.awaitingWake
        : ACTIVITY_LABEL_WEIGHTS.awaitingWakeUnnamed,
      source: "host_activity",
    };
  }
  if (activity.kind === "running_tool") {
    if (activity.progress) {
      const progress = activity.progress;
      return {
        label: progress.phase === "selecting"
          ? "Selecting files for search"
          : `Searching files · ${progress.files_searched} searched · ${progress.matches} matches${progress.files_skipped > 0 ? ` · ${progress.files_skipped} skipped` : ""}`,
        weight: ACTIVITY_LABEL_WEIGHTS.hostRunningTool,
        source: "host_activity",
      };
    }
    const part = toolPartForActivity(activity, messages);
    const fallback = activity.tool_name?.trim();
    return {
      label: part
        ? thinkingToolRunningLabel(part)
        : fallback
          ? toolRunningVerb(
              {
                id: activity.activity_id,
                toolCallId: activity.tool_call_id ?? "",
                assistantMessageId: "",
                messageId: "",
                tool: fallback,
                kind: classifyToolKind(fallback),
                status: "running",
              },
              false,
            )
          : THINKING_GENERIC_TOOL_LABEL,
      weight: ACTIVITY_LABEL_WEIGHTS.hostRunningTool,
      source: "host_activity",
    };
  }
  return undefined;
}

/** Hidden-report activity comes from host visibility flags. */
function isProvisionalCloseoutTurn(input: ThinkingActivityInput): boolean {
  const loop = input.coordinatorLoop;
  const turn = input.llmTurnActivity;
  const turnLive = turn?.status === "active" || loop?.host_turn === true;
  if (!turnLive) return false;
  return !!(turn?.provisionalHidden || loop?.provisional_hidden);
}

function provisionalCloseoutCandidate(
  input: ThinkingActivityInput,
): ActivityLabelCandidate | undefined {
  if (!isProvisionalCloseoutTurn(input)) return undefined;
  return {
    label: THINKING_DRAFTING_REPORT_LABEL,
    weight: ACTIVITY_LABEL_WEIGHTS.provisionalCloseout,
    source: "provisional_closeout",
  };
}

/** Select the first pending checklist row as planned activity. */
function progressStepCandidate(
  steps: readonly ProgressStep[] | undefined,
  progressRunning: boolean | undefined,
): ActivityLabelCandidate | undefined {
  if (!steps?.length) return undefined;
  if (progressRunning === false) return undefined;

  const live = liveProgressItems(steps);
  const label = live[0]?.label.trim();
  if (!label) return undefined;

  return {
    // Preserve the host's existing truncation marker.
    label,
    weight: ACTIVITY_LABEL_WEIGHTS.progressStepFirstOpen,
    source: "progress_step",
  };
}

function batchPhaseCandidate(
  phase: CoordinatorBatchPhase | undefined,
): ActivityLabelCandidate | undefined {
  const visible = visibleBatchPhase(phase);
  if (!visible) return undefined;
  return {
    label: batchPhaseActivityLabel(visible),
    weight: ACTIVITY_LABEL_WEIGHTS.batchPhase,
    source: "batch_phase",
  };
}

/** Review holds retain their explicit human action while model work is idle. */
function workflowPhaseCandidate(
  run: WorkflowRun | null | undefined,
  sessionId: string | undefined,
): ActivityLabelCandidate | undefined {
  if (!run) return undefined;
  const sid = sessionId?.trim();
  if (sid && run.session_id !== sid) return undefined;
  if (!ACTIVE_WORKFLOW_RUN_STATUSES.has(run.status)) return undefined;
  // The run rests on its last phase with status still "running".
  if (run.ui?.phase_terminal) return undefined;
  if (run.ui?.human_approval_awaiting) {
    return {
      label: "Waiting for your approval",
      weight: ACTIVITY_LABEL_WEIGHTS.humanReview,
      source: "workflow_phase",
    };
  }
  if (run.ui?.pending_feedback) {
    return {
      label: "Waiting for your answer",
      weight: ACTIVITY_LABEL_WEIGHTS.humanReview,
      source: "workflow_phase",
    };
  }
  const label = workflowPhaseLabel(run.ui?.current_phase_label);
  if (!label || label === "—") return undefined;
  return {
    label: label + (workflowObligationSuffix(run.ui?.phase_obligations) ||
      workflowTopologySuffix(run.ui?.topology_legs, run.current_phase)),
    weight: ACTIVITY_LABEL_WEIGHTS.workflowPhase,
    source: "workflow_phase",
  };
}

function coordinatorSurfaceCandidate(
  loop: CoordinatorLoopProgress | null | undefined,
): ActivityLabelCandidate | undefined {
  const surface = loop?.surface?.trim();
  if (!surface) return undefined;
  // Unlabeled surfaces leave other activity candidates eligible.
  const label = loop?.activity_label?.trim();
  if (!label) return undefined;

  return {
    label,
    weight: loop?.host_turn
      ? ACTIVITY_LABEL_WEIGHTS.coordinatorSurfaceHost
      : ACTIVITY_LABEL_WEIGHTS.coordinatorSurfaceIdle,
    source: "coordinator_surface",
  };
}

function workerCandidate(
  workers: readonly WorkerTask[] | undefined,
): ActivityLabelCandidate | undefined {
  const label = richWorkerActivityLabel(workers ?? []);
  if (!label) return undefined;
  return {
    label,
    weight: ACTIVITY_LABEL_WEIGHTS.workerRich,
    source: "worker",
  };
}

/** Provider waits span the host's active request lease. */
function providerWaitCandidate(
  llmTurnActivity: LlmTurnActivity | undefined,
  providers: readonly ProviderMeta[] | undefined,
  kinds: readonly ProviderKindTemplate[] | undefined,
): ActivityLabelCandidate | undefined {
  if (llmTurnActivity?.status !== "active") return undefined;
  const provider = providerLabelForId(llmTurnActivity.provider, providers, kinds);
  if (!provider) return undefined;
  const retry = providerRetryNote(llmTurnActivity.retry);
  const label = `${THINKING_PROVIDER_WAIT_VERB} ${provider}`;
  return {
    label: retry ? `${label} · ${retry}` : label,
    weight: ACTIVITY_LABEL_WEIGHTS.providerWait,
    source: "provider_wait",
  };
}

/** Retry reasons and silence durations are host observations. */
function providerRetryNote(
  retry: LlmTurnActivity["retry"],
): string | undefined {
  if (!retry?.attempt) return undefined;
  if (retry.reason === "hold" || retry.reason === "cooldown") {
    return THINKING_BUSY_VERB;
  }
  if (retry.reason === "silent") {
    const quiet = formatSilence(retry.silenceMs);
    return quiet
      ? `${THINKING_WENT_QUIET_VERB} ${quiet}, ${THINKING_RETRY_VERB} ${retry.attempt}/${retry.maxAttempts}`
      : `${THINKING_WENT_QUIET_VERB}, ${THINKING_RETRY_VERB} ${retry.attempt}/${retry.maxAttempts}`;
  }
  if (retry.reason === "unreachable") {
    return `${THINKING_NO_ANSWER_VERB}, ${THINKING_RETRY_VERB} ${retry.attempt}/${retry.maxAttempts}`;
  }
  return `${THINKING_RETRY_VERB} ${retry.attempt}/${retry.maxAttempts}`;
}

/** Silence durations display in whole seconds. */
function formatSilence(silenceMs: number | undefined): string | undefined {
  if (!silenceMs || silenceMs < 1000) return undefined;
  return `${Math.round(silenceMs / 1000)}s`;
}

function promptStartingCandidate(
  promptStarting: boolean | undefined,
): ActivityLabelCandidate | undefined {
  if (!promptStarting) return undefined;
  return {
    label: THINKING_SENDING_LABEL,
    weight: ACTIVITY_LABEL_WEIGHTS.promptStarting,
    source: "prompt_starting",
  };
}

export function collectThinkingActivityCandidates(
  input: ThinkingActivityInput,
): ActivityLabelCandidate[] {
  const messages = input.messages ?? [];
  const candidates: ActivityLabelCandidate[] = [];

  const providerWait = providerWaitCandidate(
    input.llmTurnActivity,
    input.providers,
    input.providerKinds,
  );
  if (providerWait) candidates.push(providerWait);

  const activity = hostActivityCandidate(input.activities, messages);
  if (activity) candidates.push(activity);

  const tool = runningToolCandidate(messages);
  if (tool) candidates.push(tool);

  const closeout = provisionalCloseoutCandidate(input);
  if (closeout) candidates.push(closeout);

  const progress = progressStepCandidate(
    input.progressSteps,
    input.progressRunning,
  );
  if (progress) candidates.push(progress);

  const batch = batchPhaseCandidate(input.batchPhase);
  if (batch) candidates.push(batch);

  const workflow = workflowPhaseCandidate(
    input.activeWorkflowRun,
    input.sessionId,
  );
  if (workflow) candidates.push(workflow);

  const surface = coordinatorSurfaceCandidate(input.coordinatorLoop);
  if (surface) candidates.push(surface);

  const worker = workerCandidate(input.workers);
  if (worker) candidates.push(worker);

  const prompt = promptStartingCandidate(input.promptStarting);
  if (prompt) candidates.push(prompt);

  candidates.push({
    label: THINKING_FALLBACK_LABEL,
    weight: ACTIVITY_LABEL_WEIGHTS.default,
    source: "default",
  });

  return candidates;
}

function sortCandidates(
  candidates: ActivityLabelCandidate[],
): [ActivityLabelCandidate, ...ActivityLabelCandidate[]] {
  const sourceOrder: Record<ActivityLabelSource, number> = {
    provider_wait: 0,
    host_activity: 1,
    running_tool: 2,
    provisional_closeout: 3,
    worker: 4,
    batch_phase: 5,
    coordinator_surface: 6,
    workflow_phase: 7,
    prompt_starting: 8,
    progress_step: 9,
    default: 10,
  };

  const [winner, ...rest] = [...candidates].sort((a, b) => {
    if (b.weight !== a.weight) return b.weight - a.weight;
    return sourceOrder[a.source] - sourceOrder[b.source];
  });
  if (!winner) throw new Error("Thinking activity has no candidates");
  return [winner, ...rest];
}

function maybeCombineBatchPhase(
  winner: ActivityLabelCandidate,
  runnerUp: ActivityLabelCandidate | undefined,
): string {
  if (!runnerUp || runnerUp.source !== "batch_phase") return winner.label;
  if (
    winner.source === "batch_phase" ||
    winner.source === "progress_step" ||
    winner.source === "provisional_closeout"
  ) {
    return winner.label;
  }
  if (winner.weight - runnerUp.weight > BATCH_PHASE_COMBINE_GAP) {
    return winner.label;
  }
  if (winner.label.startsWith(runnerUp.label)) return winner.label;
  return `${runnerUp.label} · ${winner.label}`;
}

/** Highest-weight candidate for these signals, uncut and unformatted. */
export function selectThinkingActivityCandidate(
  input: ThinkingActivityInput,
): ActivityLabelCandidate {
  return sortCandidates(collectThinkingActivityCandidates(input))[0];
}

export function resolveThinkingActivityLabel(
  input: ThinkingActivityInput,
): string {
  const sorted = sortCandidates(collectThinkingActivityCandidates(input));
  // Labels share one truncation and marker pass.
  return formatThinkingLabel(maybeCombineBatchPhase(sorted[0], sorted[1]));
}
