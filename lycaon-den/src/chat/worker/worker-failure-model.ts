import type { WorkerFailure, WorkerTask } from "../../api/types.ts";

export type WorkerFailureDisplay = {
  headline: string;
  message: string;
  suggestedAction?: string;
  code?: string;
};

const GENERIC_FAILED: WorkerFailureDisplay = {
  headline: "Worker failed",
  message: "Worker did not complete.",
};

const DISPATCH_REJECTED: WorkerFailureDisplay = {
  headline: "Worker dispatch rejected",
  message: "The coordinator called a worker with arguments the host rejected.",
};

/** Structured failure for a task()/delegate_dispatch reject that never enqueued a job. */
export function workerFailureFromDispatchReject(
  code: string | undefined,
): WorkerFailure {
  const trimmedCode = code?.trim() || undefined;
  return {
    code: trimmedCode,
    title: DISPATCH_REJECTED.headline,
    message: DISPATCH_REJECTED.message,
  };
}

/** User-facing copy for terminal worker execute failures (wire failure from sidecar). */
export function workerFailureDisplay(worker?: WorkerTask): WorkerFailureDisplay | undefined {
  if (!worker || (worker.status !== "failed" && worker.status !== "canceled")) {
    return undefined;
  }
  if (worker.status === "canceled") {
    return {
      headline: "Worker canceled",
      message: worker.error?.trim() || "Worker was canceled before completion.",
    };
  }
  const failure = worker.failure;
  if (failure?.title?.trim() && failure.message?.trim()) {
    return {
      headline: failure.title.trim(),
      message: failure.message.trim(),
      suggestedAction: failure.suggested_action?.trim() || undefined,
      code: failure.code?.trim() || undefined,
    };
  }
  const err = worker.error?.trim();
  if (err) {
    return {
      headline: GENERIC_FAILED.headline,
      message: err,
    };
  }
  return GENERIC_FAILED;
}

export function workerFailureStatusLines(
  display: WorkerFailureDisplay | undefined,
): string[] {
  if (!display) return [];
  const lines = [display.message];
  if (display.suggestedAction) lines.push(display.suggestedAction);
  return lines;
}
