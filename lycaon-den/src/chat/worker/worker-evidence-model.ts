import type {
  CitationGrounding,
  Message,
  WorkerSummaryMeta,
  WorkerTask,
} from "../../api/types.ts";
import { citationGroundingPresent } from "../grounding/citation-grounding-model.ts";
import {
  workerFailureDisplay,
  workerFailureStatusLines,
} from "./worker-failure-model.ts";

export type WorkerEvidenceOutcome = "traced" | "partial" | "evidence_optional" | "pending" | "failed";

/** Fallback evidence copy when the host has not attached a grounding audit. */
export type WorkerEvidenceFallbackView = {
  jobId: string;
  outcome: WorkerEvidenceOutcome;
  headline: string;
  hintCode?: string;
  statusLines: string[];
};

export function workerEvidenceSectionId(jobId: string): string {
  return `worker-evidence-${jobId.trim()}`;
}

export function workerEvidenceJobId(
  meta?: WorkerSummaryMeta,
  worker?: WorkerTask,
  jobIdFallback?: string,
): string | undefined {
  return (
    worker?.id?.trim() ||
    meta?.worker_id?.trim() ||
    jobIdFallback?.trim() ||
    undefined
  );
}

export function workerSummaryMetaForJob(
  messages: readonly Message[],
  jobId: string,
  worker?: WorkerTask,
): WorkerSummaryMeta | undefined {
  const id = jobId.trim();
  if (!id) return undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const meta = messages[i]?.worker_summary;
    if (meta?.worker_id?.trim() === id) return meta;
  }
  const recordId = worker?.worker_record_id?.trim();
  const childId = worker?.child_session_id?.trim();
  const workerName = worker?.worker_name?.trim();
  if (!recordId && !childId && !workerName) return undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const meta = messages[i]?.worker_summary;
    if (!meta) continue;
    if (
      (recordId && meta.worker_record_id?.trim() === recordId) ||
      (childId && meta.child_session_id?.trim() === childId) ||
      (workerName && meta.worker_name?.trim() === workerName)
    ) {
      return meta;
    }
  }
  return undefined;
}

/** Sealed result handle reviewed by a reviewer worker round. */
export function workerReviewedResultHandle(
  meta?: WorkerSummaryMeta,
  worker?: WorkerTask,
): string | undefined {
  return worker?.review_of?.trim() || meta?.review_of?.trim() || undefined;
}

function workerStillRunning(worker?: WorkerTask): boolean {
  if (!worker) return false;
  return (
    worker.status === "running" ||
    worker.status === "pending" ||
    worker.status === "waiting" ||
    worker.status === "held"
  );
}

export function resolveCitationGrounding(
  meta?: WorkerSummaryMeta,
  worker?: WorkerTask,
): CitationGrounding | undefined {
  if (
    meta &&
    worker &&
    meta.worker_id?.trim() &&
    meta.worker_id.trim() !== worker.id.trim()
  ) {
    return worker.result?.grounding;
  }
  return meta?.grounding ?? worker?.result?.grounding;
}

function pendingView(
  jobId: string,
  headline: string,
  statusLines: string[],
): WorkerEvidenceFallbackView {
  return {
    jobId,
    outcome: "pending",
    headline,
    statusLines,
  };
}

/** Pending/failed/unavailable copy when no structured grounding audit is on the wire. */
export function buildWorkerEvidenceFallbackView(
  meta: WorkerSummaryMeta | undefined,
  worker: WorkerTask | undefined,
  jobIdFallback?: string,
): WorkerEvidenceFallbackView | undefined {
  const jobId = workerEvidenceJobId(meta, worker, jobIdFallback);
  if (!jobId) return undefined;

  const grounding = resolveCitationGrounding(meta, worker);
  if (citationGroundingPresent(grounding)) return undefined;

  if (worker?.status === "failed" || worker?.status === "canceled") {
    const failure = workerFailureDisplay(worker);
    return {
      jobId,
      outcome: "failed",
      headline: failure?.headline ?? "Worker failed",
      hintCode: failure?.code,
      statusLines: workerFailureStatusLines(failure),
    };
  }

  if (workerStillRunning(worker)) {
    return pendingView(jobId, "Verification pending", [
      "Runs when the worker finishes and the host checks summary citations against tool history.",
    ]);
  }

  return pendingView(jobId, "Verification audit unavailable", [
    "This worker run has no grounding audit on the wire.",
  ]);
}
