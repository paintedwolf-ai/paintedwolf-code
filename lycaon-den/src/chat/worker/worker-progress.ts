import type { WorkerTask } from "../../api/types.ts";
import { workerRowStatus } from "./workers-model.ts";

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

/** Model turns the worker's round has taken; tool calls use a separate scale. */
export function formatWorkerTurns(worker: WorkerTask): string | null {
  const turns = Math.max(worker.tool_loops_used ?? 0, 0);
  if (turns === 0) return null;
  return `${turns} ${turns === 1 ? "turn" : "turns"}`;
}

export function workerTurnsAriaLabel(worker: WorkerTask): string | undefined {
  const turns = Math.max(worker.tool_loops_used ?? 0, 0);
  return turns === 0 ? undefined : `${turns} model ${turns === 1 ? "turn" : "turns"} taken`;
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
  /** The running round's tool batch. */
  batch: { done: number; total: number; ratio: number };
};

/** A running round's batch progress, counted in tool calls. */
export function workerCardProgress(
  worker: WorkerTask | undefined,
): WorkerCardProgress | undefined {
  if (!worker) return undefined;
  const batch = toolBatch(worker);
  return batch ? { batch } : undefined;
}
