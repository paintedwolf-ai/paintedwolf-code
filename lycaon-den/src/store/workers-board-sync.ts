import type { BoardView, BoardWorker, WorkerTask } from "../api/types.ts";
import { workerStoreFingerprint } from "../chat/worker/workers-model.ts";

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

function reconcileWorkerRow(row: WorkerTask, bw: BoardWorker): WorkerTask {
  const isLatestRound =
    !bw.latest_job_id?.trim() || bw.latest_job_id.trim() === row.id;
  const next: WorkerTask = {
    ...row,
    worker_record_id: bw.worker_id?.trim() || row.worker_record_id,
    worker_name: bw.name?.trim() || row.worker_name,
    child_session_id: bw.child_session_id?.trim() || row.child_session_id,
    ...(isLatestRound
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
  if (boardWorkerRows(board).length === 0) return workers as WorkerTask[];
  let changed = false;
  const next = workers.map((row) => {
    const bw = boardWorkerForTask(board, row);
    if (!bw) return row;
    const reconciled = reconcileWorkerRow(row, bw);
    if (reconciled !== row) changed = true;
    return reconciled;
  });
  return changed ? next : (workers as WorkerTask[]);
}
