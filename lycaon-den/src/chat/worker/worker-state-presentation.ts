import type {
  BoardWorker,
  WorkerResultDisposition,
  WorkerState,
  WorkerTask,
} from "../../api/types.ts";
import type { PendingWorkerApproval } from "./worker-approval-model.ts";
import { workerFailureDisplay } from "./worker-failure-model.ts";
import { workerDependencyLabel } from "./workers-model.ts";

export type WorkerDispositionBadge = {
  disposition: WorkerResultDisposition;
  label: string;
  shelveReason?: string;
};

export type WorkerUnreadBadge = {
  count: number;
  label: string;
};

function clipOneLine(text: string | undefined, max = 120): string | undefined {
  const line = text
    ?.split("\n")
    .map((part) => part.trim())
    .find(Boolean);
  if (!line) return undefined;
  return line.length > max ? `${line.slice(0, max - 1)}…` : line;
}

/** Derived worker state from board or round facts; never marks change ops as execution. */
function derivedWorkerState(
  worker?: WorkerTask,
  boardWorker?: BoardWorker,
): WorkerState | undefined {
  if (boardWorker?.state) return boardWorker.state;
  if (!worker) return undefined;
  if (worker.status === "failed") return "failed";
  if (worker.status === "canceled") return "closed";
  if (worker.status === "running" || worker.status === "waiting" || worker.status === "held") {
    return "working";
  }
  if (worker.status === "pending") {
    return worker.dependencies?.some((d) => d.state !== "ready")
      ? "waiting"
      : "working";
  }
  if (worker.status === "complete") {
    const resStatus = worker.result?.status?.trim().toLowerCase();
    if (resStatus === "needs_decision" || resStatus === "needs_input") {
      return "asking";
    }
    const disp = worker.result_disposition;
    if (disp === "unaccounted" || disp === "open") return "delivered";
    if (disp === "landed" || disp === "rejected" || disp === "shelved" || disp === "accounted") {
      return "idle";
    }
  }
  return undefined;
}

/** Sentence-case status badge label when collaboration state facts are present. */
export function workerStateBadgeLabel(
  worker?: WorkerTask,
  boardWorker?: BoardWorker,
  pendingApproval?: PendingWorkerApproval,
): string | undefined {
  if (pendingApproval || boardWorker?.reason === "approval_wait") {
    return "Waiting for approval";
  }
  const parkedReason =
    boardWorker?.parked_reason?.trim() || worker?.parked_reason?.trim();
  if (
    boardWorker?.reason === "parked" ||
    worker?.status === "waiting" ||
    Boolean(parkedReason)
  ) {
    return "Parked";
  }
  const state = derivedWorkerState(worker, boardWorker);
  switch (state) {
    case "planned":
      return "Planned";
    case "waiting":
      return "Waiting";
    case "working":
      return boardWorker?.reason === "queued" || worker?.status === "pending"
        ? "Queued"
        : "Working";
    case "asking":
      return "Asking";
    case "delivered":
      return "Delivered";
    case "idle":
      return "Idle";
    case "failed":
      return "Failed";
    case "recovering":
      return "Recovering";
    case "closed":
      return worker?.status === "canceled" ? undefined : "Closed";
    default:
      return undefined;
  }
}

/** Visible disposition of the worker's latest result, distinct from unread messages. */
export function workerDispositionPresentation(
  worker?: WorkerTask,
  boardWorker?: BoardWorker,
): WorkerDispositionBadge | undefined {
  const disp = boardWorker?.result_disposition ?? worker?.result_disposition;
  if (!disp) return undefined;
  const shelveReason =
    boardWorker?.shelve_reason?.trim() || worker?.shelve_reason?.trim();
  switch (disp) {
    case "unaccounted":
      return { disposition: disp, label: "Not accounted for" };
    case "accounted":
      return { disposition: disp, label: "Accounted for" };
    case "open":
      return { disposition: disp, label: "Open changes" };
    case "landed":
      return { disposition: disp, label: "Landed" };
    case "rejected":
      return { disposition: disp, label: "Rejected" };
    case "shelved":
      return {
        disposition: disp,
        label: shelveReason ? `Shelved · ${clipOneLine(shelveReason, 64)}` : "Shelved",
        shelveReason,
      };
  }
}

/** Unread worker messages awaiting coordinator turn delivery. */
export function workerUnreadPresentation(
  boardWorker?: BoardWorker,
): WorkerUnreadBadge | undefined {
  const count = boardWorker?.unread_message_count ?? 0;
  if (count <= 0) return undefined;
  return {
    count,
    label: count === 1 ? "1 unread message" : `${count} unread messages`,
  };
}

/** High-priority state and result line rendered from backend worker facts. */
export function workerFactActivityLine(
  worker?: WorkerTask,
  boardWorker?: BoardWorker,
  pendingApproval?: PendingWorkerApproval,
): string | null {
  if (pendingApproval) {
    return `Waiting for approval · ${pendingApproval.commandSummary}`;
  }
  const state = derivedWorkerState(worker, boardWorker);
  if (state === "recovering") {
    return boardWorker?.state_label?.trim() || "Recovering interrupted workspace settlement";
  }
  if (state === "asking") {
    const question = clipOneLine(boardWorker?.question);
    return question ? `Asking: ${question}` : "Waiting for an answer";
  }
  const parkedReason =
    boardWorker?.parked_reason?.trim() || worker?.parked_reason?.trim();
  if (
    boardWorker?.reason === "parked" ||
    worker?.status === "waiting" ||
    Boolean(parkedReason)
  ) {
    return parkedReason ? `Parked · ${parkedReason}` : "Parked — waiting for condition";
  }
  if (state === "failed" || worker?.status === "failed") {
    const failure = workerFailureDisplay(worker);
    if (failure?.message) return clipOneLine(failure.message, 120) ?? failure.message;
    return boardWorker?.state_label?.trim() || "Worker failed";
  }
  if (state === "waiting") {
    return (
      (worker ? workerDependencyLabel(worker) : undefined) ??
      boardWorker?.state_label?.trim() ??
      "Waiting on prerequisites"
    );
  }
  if (state === "working") {
    const round = boardWorker?.round ?? worker?.round ?? 1;
    const about = clipOneLine(boardWorker?.about);
    if (round > 1 && about) return `Round ${round} · ${about}`;
    return null;
  }
  const disp = boardWorker?.result_disposition ?? worker?.result_disposition;
  if (disp === "shelved") {
    const reason =
      boardWorker?.shelve_reason?.trim() || worker?.shelve_reason?.trim();
    const summary = clipOneLine(worker?.result?.summary, 80);
    if (reason && summary) return `${summary} · Shelved — ${reason}`;
    return reason ? `Shelved — ${reason}` : summary ?? "Shelved";
  }
  if (worker?.status === "complete") {
    const summary = clipOneLine(worker.result?.summary, 120);
    if (summary) return summary;
  }
  if (boardWorker?.workspace_state === "refreshed") return "Workspace refreshed";
  if (boardWorker?.workspace_state === "reset") return "Workspace reset";
  return null;
}
