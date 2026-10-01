import type { BoardView, BoardWorkerRosterEntry, WorkerTask } from "../api/types.ts";
import { workerStoreFingerprint } from "../chat/worker/workers-model.ts";

function boardRosterRows(board?: BoardView): BoardWorkerRosterEntry[] {
  const raw = board?.roster;
  if (!Array.isArray(raw)) return [];
  return raw;
}

/** Settled; a worker never leaves one of these once reached. */
const TERMINAL_WORKER_STATUSES = new Set<WorkerTask["status"]>([
  "complete",
  "failed",
  "canceled",
]);

function reconcileWorkerRow(
  row: WorkerTask,
  snap: BoardWorkerRosterEntry,
): WorkerTask {
  const mergeStatus = snap.merge_status ?? row.merge_status;
  // The host's publish debounce can deliver a board built before this worker
  // settled after a fresher one, and a settled worker emits no further events.
  // A terminal row never moves back.
  const status =
    TERMINAL_WORKER_STATUSES.has(row.status) && !TERMINAL_WORKER_STATUSES.has(snap.status)
      ? row.status
      : snap.status;
  const next: WorkerTask = {
    ...row,
    status,
    merge_status: mergeStatus,
    dependencies: snap.dependencies ?? row.dependencies,
  };
  if (workerStoreFingerprint(row) === workerStoreFingerprint(next)) return row;
  return next;
}

export function reconcileWorkersFromBoard(
  workers: readonly WorkerTask[],
  board?: BoardView,
): WorkerTask[] {
  const rows = boardRosterRows(board);
  if (rows.length === 0) return workers as WorkerTask[];
  const byId = new Map(rows.map((t) => [t.worker_id, t]));
  let changed = false;
  const next = workers.map((row) => {
    const snap = byId.get(row.id);
    if (!snap) return row;
    const reconciled = reconcileWorkerRow(row, snap);
    if (reconciled !== row) changed = true;
    return reconciled;
  });
  return changed ? next : (workers as WorkerTask[]);
}
