import type { WorkerSummaryMeta, WorkerTask } from "../../api/types.ts";
import {
  type WorkerRowStatus,
  workerRowStatus,
} from "./workers-model.ts";

export function workerSummaryTaskStatus(
  meta?: WorkerSummaryMeta,
  worker?: WorkerTask,
): WorkerRowStatus {
  if (worker) return workerRowStatus(worker);
  const status = meta?.status?.trim().toLowerCase();
  if (status === "canceled") return "canceled";
  if (status === "failed" || status === "held") {
    return "error";
  }
  if (status === "needs_decision") return "needs_decision";
  if (status === "partial") return "partial";
  if (status === "open") return "open";
  if (status === "complete") return "done";
  return "running";
}
