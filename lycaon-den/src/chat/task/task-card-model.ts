import type { BoardWorker, Message, WorkerTask } from "../../api/types.ts";
import type { PendingWorkerApproval } from "../worker/worker-approval-model.ts";
import { workerBranchActivityLine } from "../worker/worker-branch-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import { isTaskToolName } from "../tool/tool-part-model.ts";
import { lastWorkerToolActivity } from "../worker/worker-activity.ts";
import {
  generatingTokensLabel,
  liveGeneratingTokensFromMessages,
} from "../transcript/projection/generating-tokens.ts";
import { workerFailureDisplay } from "../worker/worker-failure-model.ts";
import { workerFactActivityLine } from "../worker/worker-state-presentation.ts";
import { workerRowStatus } from "../worker/workers-model.ts";
import { workerSummaryTaskStatus } from "../worker/worker-summary-model.ts";
import { formatBytes } from "../../format/format-bytes.ts";

export type TaskActivityContext = {
  worker?: WorkerTask;
  boardWorker?: BoardWorker;
  pendingApproval?: PendingWorkerApproval;
  workerStatus?: string | null;
  lastActivity?: string | null;
  workerMessages?: readonly Message[];
};

function trimLine(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : null;
}

function duplicatesTaskDescription(part: ToolPartView, activity: string): boolean {
  const desc = taskDescription(part).trim().toLowerCase();
  const line = activity.trim().toLowerCase();
  return desc.length > 0 && desc === line;
}

function firstUsefulActivity(
  part: ToolPartView,
  candidates: (string | null | undefined)[],
): string | null {
  for (const candidate of candidates) {
    if (!candidate) continue;
    if (!duplicatesTaskDescription(part, candidate)) return candidate;
  }
  return null;
}

function statusFallback(part: ToolPartView, worker?: WorkerTask): string {
  const status = taskStatus(part);
  if (status === "running") return "Spawning worker…";
  if (status === "error") {
    const failure = workerFailureDisplay(worker);
    if (failure?.message) {
      return failure.message.length > 96
        ? `${failure.message.slice(0, 93)}…`
        : failure.message;
    }
    const err = trimLine(part.error);
    if (err) return err.length > 96 ? `${err.slice(0, 93)}…` : err;
    return "Worker failed";
  }
  if (status === "done") return "Completed";
  return "In progress…";
}

export function taskStatus(part: ToolPartView): string {
  if (part.status === "running") return "running";
  if (part.status === "error") return "error";
  return "done";
}

/** Current terminal, merge, and later-round facts supersede an earlier projection. */
export function taskCardStatus(
  part: ToolPartView,
  worker?: WorkerTask,
  boardWorker?: BoardWorker,
): string {
  if (boardWorker?.state === "asking") return "needs_decision";
  if (boardWorker?.state === "failed" || boardWorker?.state === "recovering") {
    return "error";
  }
  if (worker) {
    const summaryJob = part.workerSummary?.worker_id?.trim();
    const newerRound =
      (worker.round ?? 1) > (part.workerSummary?.round ?? 1) ||
      Boolean(summaryJob && worker.id !== summaryJob);
    if (newerRound) return workerRowStatus(worker);
    if (worker.status === "canceled" || worker.status === "failed") {
      return workerRowStatus(worker);
    }
    if (
      worker.status === "complete" &&
      (worker.merge_status || worker.result_disposition)
    ) {
      return workerRowStatus(worker);
    }
  }
  if (part.workerSummary) return workerSummaryTaskStatus(part.workerSummary);
  if (worker && workerRowStatus(worker) === "running") return "running";
  return taskStatus(part);
}

export function taskDescription(part: ToolPartView, worker?: WorkerTask): string {
  const briefGoal = worker?.brief
    ?.split("\n")
    .find((line) => line.trim())
    ?.trim();
  if (briefGoal) return briefGoal;
  const args = part.args ?? {};
  const brief = args.brief;
  const goal =
    brief && typeof brief === "object" && !Array.isArray(brief)
      ? (brief as { goal?: unknown }).goal
      : undefined;
  const desc = (typeof goal === "string" && goal.split("\n")[0]) || part.title;
  return desc?.trim() || "Worker task";
}

export function subagentType(part: ToolPartView): string {
  const tool = part.tool.toLowerCase();
  if (tool === "delegate_dispatch") return "delegation";
  const args = part.args ?? {};
  if (typeof args.subagent_type === "string" && args.subagent_type.trim()) {
    return args.subagent_type.trim();
  }
  if (typeof args.agent_type === "string" && args.agent_type.trim()) {
    return args.agent_type.trim();
  }
  return "worker";
}

/** Card title agent from the worker roster or the immutable call snapshot. */
export function taskAgentType(part: ToolPartView, worker?: WorkerTask): string {
  const fromWorker = worker?.agent_type?.trim();
  const workerName = worker?.worker_name?.trim();
  if (fromWorker && workerName && workerName !== fromWorker) {
    return `${workerName} · ${fromWorker}`;
  }
  if (fromWorker) return fromWorker;
  const fromArgs = subagentType(part);
  if (fromArgs !== "worker") return fromArgs;
  return "worker";
}

export function taskActivityDisplay(
  part: ToolPartView,
  ctx?: TaskActivityContext,
): string {
  const preparation = ctx?.worker?.workspace_preparation;
  if (preparation) {
    const current = formatBytes(preparation.bytes);
    const total = formatBytes(preparation.total_bytes);
    const amount = total
      ? `${current || "0 B"} of ${total}`
      : current || (preparation.files > 0 ? `${preparation.files} files` : "");
    if (preparation.stage === "probing") return "Checking workspace storage…";
    if (preparation.stage === "surveying_source") {
      return `Surveying workspace${amount ? ` · ${amount}` : ""}`;
    }
    const action =
      preparation.strategy === "direct_copy"
        ? "Copying isolated workspace"
        : "Cloning isolated workspace";
    return `${action}${amount ? ` · ${amount}` : ""}`;
  }

  // Live token activity takes priority while the worker is generating.
  const generating = generatingTokensLabel(
    liveGeneratingTokensFromMessages(ctx?.workerMessages),
  );
  if (generating) return generating;

  const factLine = workerFactActivityLine(
    ctx?.worker,
    ctx?.boardWorker,
    ctx?.pendingApproval,
  );
  if (factLine && !duplicatesTaskDescription(part, factLine)) return factLine;

  const branchLine = ctx?.worker ? workerBranchActivityLine(ctx.worker) : null;
  if (branchLine) return branchLine;

  const fromCtx = firstUsefulActivity(part, [ctx?.lastActivity]);
  if (fromCtx) return fromCtx;

  const fromWorker = firstUsefulActivity(part, [
    ctx?.workerMessages ? lastWorkerToolActivity(ctx.workerMessages) : null,
  ]);
  if (fromWorker) return fromWorker;

  const workerStatus = ctx?.workerStatus?.trim().toLowerCase();
  if (workerStatus) {
    const agent = taskAgentType(part, ctx?.worker);
    if (workerStatus === "running") return `${agent} · running`;
    if (workerStatus === "needs_decision") return `${agent} · needs a decision`;
    if (workerStatus === "done") return `${agent} · finished`;
    if (workerStatus === "open") return `${agent} · open on branch`;
    if (workerStatus === "partial") return `${agent} · finished (unverified)`;
    if (workerStatus === "error") {
      const failure = workerFailureDisplay(ctx?.worker);
      if (failure?.headline) return `${agent} · ${failure.headline.toLowerCase()}`;
      return `${agent} · failed`;
    }
    return `${agent} · ${workerStatus}`;
  }

  return statusFallback(part, ctx?.worker);
}

export function isTaskToolPart(part: ToolPartView): boolean {
  return isTaskToolName(part.tool);
}
