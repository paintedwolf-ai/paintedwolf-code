import type { Message } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { upsertMessageRow } from "../transcript/projection/message-row.ts";
import { contiguousWindowRows, precedesLiveTail } from "../transcript/layout/transcript-window.ts";
import {
  latestMessageSnapshot,
  messageLiveFieldsEqual,
} from "../transcript/projection/messages-equal.ts";

/** Pending rows per worker, keyed by row id. */
const pendingByWorker = new Map<string, Map<string, Message>>();
let flushScheduled = false;
let flushTarget: AppStore | undefined;
let flushGeneration = 0;

export function queueWorkerTranscriptPatch(
  appStore: AppStore,
  workerId: string,
  row: Message,
): boolean {
  const id = row.id?.trim();
  if (!id) return false;
  let pending = pendingByWorker.get(workerId);
  if (!pending) {
    pending = new Map<string, Message>();
    pendingByWorker.set(workerId, pending);
  }
  const queued = pending.get(id);
  if (queued) {
    // Pending snapshots retain the newest row revision.
    const kept = latestMessageSnapshot(queued, row);
    if (kept === queued || messageLiveFieldsEqual(queued, kept)) return false;
    pending.set(id, kept);
  } else {
    const settled = appStore.state.workerTranscripts[workerId]?.rows.find(
      (m) => m.id === id,
    );
    if (settled) {
      const kept = latestMessageSnapshot(settled, row);
      if (kept === settled || messageLiveFieldsEqual(settled, kept)) {
        return false;
      }
      pending.set(id, kept);
    } else {
      pending.set(id, row);
    }
  }
  flushTarget = appStore;
  scheduleFlush();
  return true;
}

/**
 * The drawer's rows: one contiguous range from the oldest resident row, plus live rows
 * still waiting for their store flush. Across a tail gap the range ends before the tail.
 */
export function workerTranscriptRowsForDisplay(
  appStore: AppStore,
  workerId: string,
): readonly Message[] | undefined {
  const id = workerId.trim();
  if (!id) return undefined;
  const entry = appStore.state.workerTranscripts[id];
  if (entry?.window.hasTailGap) return contiguousWindowRows(entry.window);
  const settled = entry?.rows;
  const pending = pendingByWorker.get(id);
  if (!pending?.size) return settled;
  const resident = new Set(settled?.map((row) => row.id));
  // Pending rows overlay the store without mutating it.
  let rows = settled ? settled.slice() : [];
  for (const row of pending.values()) {
    if (entry && !resident.has(row.id) && precedesLiveTail(entry.window, row)) continue;
    rows = upsertMessageRow(rows, row);
  }
  return rows;
}

export function flushWorkerTranscriptCoalesce(appStore?: AppStore): void {
  flushGeneration += 1;
  flushScheduled = false;
  const store = appStore ?? flushTarget;
  if (!store || pendingByWorker.size === 0) {
    pendingByWorker.clear();
    flushTarget = undefined;
    return;
  }
  for (const [workerId, rows] of pendingByWorker) {
    store.actions.applyWorkerTranscriptRows(workerId, [...rows.values()]);
  }
  pendingByWorker.clear();
  flushTarget = undefined;
}

export function cancelWorkerTranscriptCoalesce(): void {
  flushGeneration += 1;
  flushScheduled = false;
  pendingByWorker.clear();
  flushTarget = undefined;
}

function scheduleFlush(): void {
  if (flushScheduled) return;
  flushScheduled = true;
  const generation = flushGeneration;
  queueMicrotask(() => {
    // Canceled callbacks cannot change a newer batch's scheduling state.
    if (generation !== flushGeneration) return;
    flushScheduled = false;
    if (flushTarget) flushWorkerTranscriptCoalesce(flushTarget);
  });
}
