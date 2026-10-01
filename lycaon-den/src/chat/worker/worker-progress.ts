import type { WorkerTask } from "../../api/types.ts";
import { workerRowStatus } from "./workers-model.ts";

/**
 * Rounds before the ceiling at which the host tells a worker its runway is
 * low; mirrors backend `spawn.WorkerRunway` (a quarter of the ceiling, 2–10).
 */
export function workerRunway(max: number): number {
  if (max <= 1) return 0;
  const runway = Math.min(Math.max(Math.ceil(max / 4), 2), 10);
  return runway >= max ? max - 1 : runway;
}

function effectiveMax(worker: WorkerTask): number | null {
  const max = worker.max_tool_loops;
  if (max == null || max <= 0) return null;
  return max;
}

function effectiveUsed(worker: WorkerTask, max: number): number {
  const used = worker.tool_loops_used ?? 0;
  return Math.min(Math.max(used, 0), max);
}

export function workerBudgetRemaining(worker: WorkerTask): number | null {
  const max = effectiveMax(worker);
  if (max == null) return null;
  return max - effectiveUsed(worker, max);
}

export function isWorkerBudgetLow(worker: WorkerTask): boolean {
  if (workerRowStatus(worker) !== "running") return false;
  const max = effectiveMax(worker);
  const remaining = workerBudgetRemaining(worker);
  if (max == null || remaining == null) return false;
  return remaining <= workerRunway(max);
}

/** Ceiling a running worker has asked its coordinator for, if unanswered. */
export function workerRequestedMax(worker: WorkerTask): number | null {
  if (workerRowStatus(worker) !== "running") return null;
  return worker.budget_request?.requested_max ?? null;
}

function isBudgetCapPartial(worker: WorkerTask, used: number, max: number): boolean {
  if (workerRowStatus(worker) === "running") return false;
  if (worker.result?.status?.trim().toLowerCase() !== "partial") return false;
  return used >= max;
}

/** Lifetime tool calls this worker has settled. */
function toolCallsUsed(worker: WorkerTask): number {
  return Math.max(worker.tool_calls_used ?? 0, 0);
}

/** Active round progress, absent between tool batches. */
function toolBatch(
  worker: WorkerTask,
): { done: number; total: number; ratio: number } | null {
  if (workerRowStatus(worker) !== "running") return null;
  const total = Math.max(worker.turn_tool_calls ?? 0, 0);
  if (total <= 0) return null;
  const done = Math.min(Math.max(worker.turn_tools_done ?? 0, 0), total);
  return { done, total, ratio: done / total };
}

/** Rounds against their budget; tool-call counts use a separate scale. */
export function formatWorkerBudget(worker: WorkerTask): string | null {
  const max = effectiveMax(worker);
  if (max == null) return null;
  const used = effectiveUsed(worker, max);
  const text = `${used}/${max}`;
  const requested = workerRequestedMax(worker);
  if (requested != null) return `${text} · asked for ${requested}`;
  return isBudgetCapPartial(worker, used, max) ? `${text} · cap` : text;
}

export function workerBudgetAriaLabel(worker: WorkerTask): string | undefined {
  const max = effectiveMax(worker);
  if (max == null) return undefined;
  const label = `Worker budget: ${effectiveUsed(worker, max)} of ${max} tool rounds used`;
  const requested = workerRequestedMax(worker);
  return requested == null ? label : `${label}; asked for ${requested}`;
}

/** Settled calls and active batch progress share the tool-call unit. */
export function formatWorkerToolCalls(worker: WorkerTask): string | null {
  const calls = toolCallsUsed(worker);
  const batch = toolBatch(worker);
  if (calls === 0 && !batch) return null;
  const total = `${calls} ${calls === 1 ? "tool" : "tools"}`;
  return batch ? `${total} · ${batch.done}/${batch.total}` : total;
}

export function workerToolCallsAriaLabel(worker: WorkerTask): string | undefined {
  const calls = toolCallsUsed(worker);
  const batch = toolBatch(worker);
  if (calls === 0 && !batch) return undefined;
  const total = `${calls} tool calls run`;
  return batch
    ? `${total}, ${batch.done} of ${batch.total} in this round`
    : total;
}

export type WorkerCardProgress = {
  /** Tool rounds spent against the budget. */
  used: number;
  max: number;
  /** Budget fill, 0–1. A finished worker reads full whatever it spent. */
  ratio: number;
  /** In-flight batch, or null between rounds. */
  batch: { done: number; total: number; ratio: number } | null;
};

/** Budget progress counts rounds; batch progress counts tool calls. */
export function workerCardProgress(
  worker: WorkerTask | undefined,
): WorkerCardProgress | undefined {
  if (!worker) return undefined;
  const max = effectiveMax(worker);
  if (max == null) return undefined;
  const used = effectiveUsed(worker, max);
  const finished = workerRowStatus(worker) !== "running";
  return {
    used,
    max,
    ratio: finished ? 1 : used / max,
    batch: toolBatch(worker),
  };
}
