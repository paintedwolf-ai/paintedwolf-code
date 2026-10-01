import type { WorkerTask } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { pendingCheckpointsForSessionView } from "../checkpoint/checkpoint-session-scope.ts";
import {
  dedupeWorkersById,
  failedDispatchWorkersFromMessages,
  sortWorkerTasks,
  workerStoreFingerprint,
  workersForSession,
} from "../worker/workers-model.ts";

/** Session roster plus derived failed-dispatch rows for tab/drawer/card binding. */
export function sessionWorkers(appStore: AppStore, sessionId: string) {
  const rows = sortWorkerTasks(
    dedupeWorkersById([
      ...workersForSession(appStore.state.workers, sessionId),
      ...failedDispatchWorkersFromMessages(sessionId, appStore.state.messages),
    ]),
  );
  // Fingerprint reads card fields so store patches to them invalidate callers.
  for (const row of rows) {
    void workerStoreFingerprint(row);
  }
  return rows;
}

/** Memo equality for a session roster: the card fields decide, not the rebuilt array. */
export function sameSessionWorkers(
  before: readonly WorkerTask[] | undefined,
  after: readonly WorkerTask[],
): boolean {
  if (!before || before.length !== after.length) return false;
  return before.every((row, index) => {
    const next = after[index]!;
    return row === next || workerStoreFingerprint(row) === workerStoreFingerprint(next);
  });
}

export function pendingCheckpointsForSession(
  appStore: AppStore,
  sessionId: string,
  opts?: { exact?: boolean },
) {
  return pendingCheckpointsForSessionView(
    appStore.state.pendingCheckpoints,
    sessionId,
    appStore.state.workers,
    opts,
  );
}
