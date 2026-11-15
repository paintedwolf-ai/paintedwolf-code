import type {
  BoardView,
  BoardWorker,
  BoardWorkerRosterEntry,
  WorkerTask,
} from "../api/types.ts";
import { workerStoreFingerprint } from "../chat/worker/workers-model.ts";

function boardRosterRows(board?: BoardView): BoardWorkerRosterEntry[] {
  const raw = board?.roster;
  if (!Array.isArray(raw)) return [];
  return raw;
}

function boardWorkerRows(board?: BoardView): BoardWorker[] {
  const raw = board?.workers;
  if (!Array.isArray(raw)) return [];
  return raw;
}

/** Match the board's canonical worker view to a session worker row. */
export function boardWorkerForTask(
  board: BoardView | undefined,
  worker: WorkerTask | undefined,
): BoardWorker | undefined {
  if (!worker) return undefined;
  const rows = boardWorkerRows(board);
  if (rows.length === 0) return undefined;
  const jobId = worker.id?.trim();
  const recordId = worker.worker_record_id?.trim();
  const childId = worker.child_session_id?.trim();
  const name = worker.worker_name?.trim();
  return rows.find(
    (bw) =>
      (jobId && bw.latest_job_id?.trim() === jobId) ||
      (recordId && bw.worker_id?.trim() === recordId) ||
      (childId && bw.child_session_id?.trim() === childId) ||
      (name && bw.name.trim() === name),
  );
}

/** Settled; a job row never leaves one of these once reached. */
const TERMINAL_WORKER_STATUSES = new Set<WorkerTask["status"]>([
  "complete",
  "failed",
  "canceled",
]);

function reconcileWorkerRow(
  row: WorkerTask,
  snap: BoardWorkerRosterEntry | undefined,
  bw: BoardWorker | undefined,
): WorkerTask {
  const mergeStatus = snap?.merge_status ?? row.merge_status;
  // The host's publish debounce can deliver a board built before this job
  // settled after a fresher one, and a settled job emits no further events.
  const status = snap
    ? TERMINAL_WORKER_STATUSES.has(row.status) &&
      !TERMINAL_WORKER_STATUSES.has(snap.status)
      ? row.status
      : snap.status
    : row.status;
  const isLatestRound =
    !bw?.latest_job_id?.trim() || bw.latest_job_id.trim() === row.id;
  const next: WorkerTask = {
    ...row,
    status,
    merge_status: mergeStatus,
    dependencies: snap?.dependencies ?? row.dependencies,
    worker_record_id: bw?.worker_id?.trim() || row.worker_record_id,
    worker_name: bw?.name?.trim() || row.worker_name,
    child_session_id: bw?.child_session_id?.trim() || row.child_session_id,
    ...(isLatestRound && bw
      ? {
          round: bw.round ?? row.round,
          result_handle: bw.result_handle?.trim() || row.result_handle,
          result_disposition: bw.result_disposition ?? row.result_disposition,
          shelve_reason: bw.shelve_reason?.trim() || row.shelve_reason,
          parked_reason:
            bw.parked_reason?.trim() ||
            (bw.reason === "parked" ? row.parked_reason : undefined),
          parked_until_at:
            bw.parked_until_at?.trim() ||
            (bw.reason === "parked" ? row.parked_until_at : undefined),
        }
      : {}),
  };
  if (workerStoreFingerprint(row) === workerStoreFingerprint(next)) return row;
  return next;
}

export function reconcileWorkersFromBoard(
  workers: readonly WorkerTask[],
  board?: BoardView,
): WorkerTask[] {
  const roster = boardRosterRows(board);
  const bws = boardWorkerRows(board);
  if (roster.length === 0 && bws.length === 0) return workers as WorkerTask[];
  const byId = new Map(roster.map((t) => [t.worker_id, t]));
  let changed = false;
  const next = workers.map((row) => {
    const snap = byId.get(row.id);
    const bw = boardWorkerForTask(board, row);
    if (!snap && !bw) return row;
    const reconciled = reconcileWorkerRow(row, snap, bw);
    if (reconciled !== row) changed = true;
    return reconciled;
  });
  return changed ? next : (workers as WorkerTask[]);
}
