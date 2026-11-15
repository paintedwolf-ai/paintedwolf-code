import type {
  BoardView,
  BoardWorker,
  BoardWorkerRosterEntry,
  CheckpointKind,
  WorkerTask,
} from "../../api/types.ts";
import type { PendingCheckpoint } from "../checkpoint/checkpoint-model.ts";
import { collapseWorkersByIdentity } from "./worker-identity.ts";

const WORKER_APPROVAL_KINDS = new Set<CheckpointKind>([
  "tool_approval",
]);

export type PendingWorkerApproval = {
  jobId: string;
  childSessionId: string;
  checkpointId: string;
  kind: CheckpointKind;
  commandSummary: string;
};

function commandSummary(checkpoint: PendingCheckpoint): string {
  const approval = checkpoint.tool_approval;
  return (
    approval?.plan.presentation.command?.trim() ||
    approval?.plan.subject.title?.trim() ||
    approval?.plan.presentation.tool?.trim() ||
    "Approval needed"
  );
}

/** Join authoritative checkpoint SSE rows to canonical worker child sessions. */
export function pendingWorkerApprovals(
  workers: readonly WorkerTask[],
  checkpoints: readonly PendingCheckpoint[],
): PendingWorkerApproval[] {
  const workerByChildSession = new Map<string, WorkerTask>();
  for (const worker of collapseWorkersByIdentity(workers)) {
    const child = worker.child_session_id?.trim();
    if (child) workerByChildSession.set(child, worker);
  }

  const out: PendingWorkerApproval[] = [];
  for (const checkpoint of checkpoints) {
    if (checkpoint.status !== "pending" || !WORKER_APPROVAL_KINDS.has(checkpoint.kind)) {
      continue;
    }
    const childSessionId = checkpoint.sessionId.trim();
    const worker = workerByChildSession.get(childSessionId);
    if (!worker) continue;
    out.push({
      jobId: worker.id,
      childSessionId,
      checkpointId: checkpoint.checkpointId,
      kind: checkpoint.kind,
      commandSummary: commandSummary(checkpoint),
    });
  }
  return out;
}

export function pendingWorkerApprovalByJob(
  workers: readonly WorkerTask[],
  checkpoints: readonly PendingCheckpoint[],
): ReadonlyMap<string, PendingWorkerApproval> {
  return new Map(
    pendingWorkerApprovals(workers, checkpoints).map((approval) => [
      approval.jobId,
      approval,
    ]),
  );
}

/** Project the SSE join onto the board roster and workers without mutating the sidecar snapshot. */
export function boardWithWorkerApprovalState(
  board: BoardView | undefined,
  workers: readonly WorkerTask[],
  checkpoints: readonly PendingCheckpoint[],
): BoardView | undefined {
  if (!board?.roster && !board?.workers) return board;
  const approvals = pendingWorkerApprovals(workers, checkpoints);
  const blockedJobs = new Set(approvals.map((approval) => approval.jobId));
  const blockedChildren = new Set(
    approvals.map((approval) => approval.childSessionId),
  );
  const roster: BoardWorkerRosterEntry[] | undefined = board.roster?.map(
    (row) => ({
      ...row,
      blocked_on_approval: blockedJobs.has(row.worker_id) || undefined,
    }),
  );
  const boardWorkers: BoardWorker[] | undefined = board.workers?.map((bw) => {
    const child = bw.child_session_id?.trim() ?? "";
    const latestJob = bw.latest_job_id?.trim() ?? "";
    const waitingForApproval =
      bw.state === "working" &&
      ((child && blockedChildren.has(child)) ||
        (latestJob && blockedJobs.has(latestJob)));
    if (!waitingForApproval) return bw;
    return {
      ...bw,
      reason: "approval_wait",
      state_label: "Waiting for approval",
    };
  });
  return {
    ...board,
    ...(roster ? { roster } : {}),
    ...(boardWorkers ? { workers: boardWorkers } : {}),
  };
}
