import {
  EMPTY_APP_STATE_V1,
  type AppStatePatch,
  type DenAppStateV1,
} from "../../shared/app-state-types.ts";
import { loadAppState, patchAppState } from "../platform/persistence/app-state.ts";

let snapshot: DenAppStateV1 = { ...EMPTY_APP_STATE_V1 };
let snapshotRevision = 0;

const APP_STATE_WRITE_DEBOUNCE_MS = 250;
const APP_STATE_WRITE_MAX_DELAY_MS = 2_000;

let pendingPatch: AppStatePatch | null = null;
let flushing: Promise<void> | null = null;
let flushTimer: ReturnType<typeof setTimeout> | undefined;
let firstQueuedAt = 0;
let waiters: Array<{ resolve: () => void; reject: (error: unknown) => void }> = [];
let retryBlocked = false;

/** In-memory app state shared by every store. */
export function getAppStateSnapshot(): DenAppStateV1 {
  return snapshot;
}

/** Whether a key's snapshot value may not have reached disk yet. */
export function appStateUnwritten(key: keyof DenAppStateV1): boolean {
  return flushing !== null || (pendingPatch !== null && key in pendingPatch);
}

export function setAppStateSnapshot(next: DenAppStateV1): void {
  snapshot = next;
  snapshotRevision++;
}

export function resetAppStateSnapshotForTests(
  next: DenAppStateV1 = { ...EMPTY_APP_STATE_V1 },
): void {
  snapshot = next;
  snapshotRevision = 0;
  pendingPatch = null;
  flushing = null;
  clearTimeout(flushTimer);
  flushTimer = undefined;
  firstQueuedAt = 0;
  waiters = [];
  retryBlocked = false;
}

export async function loadSharedAppState(): Promise<DenAppStateV1> {
  // Disk cannot supersede preferences still waiting to be written.
  if (pendingPatch !== null || flushing !== null) return snapshot;
  const revisionAtStart = snapshotRevision;
  const loaded = await loadAppState();
  if (revisionAtStart !== snapshotRevision) {
    return snapshot;
  }
  snapshot = loaded;
  return snapshot;
}

export function persistAppState(
  partial: Partial<DenAppStateV1>,
): Promise<void> {
  const patch: AppStatePatch = {};
  const next: DenAppStateV1 = { ...snapshot };

  for (const key of Object.keys(partial) as (keyof DenAppStateV1)[]) {
    if (key === "version") continue;
    const value = partial[key];
    // Undefined values become null deletions in the host patch.
    (patch as Record<string, unknown>)[key] = value === undefined ? null : value;
    if (value === undefined) delete next[key];
    else (next as Record<string, unknown>)[key] = value;
  }

  snapshot = next;
  snapshotRevision++;

  pendingPatch = { ...(pendingPatch ?? {}), ...patch };
  const persisted = new Promise<void>((resolve, reject) => {
    waiters.push({ resolve, reject });
  });
  scheduleFlush();
  return persisted;
}

async function drainPendingPatches(): Promise<void> {
  while (pendingPatch !== null) {
    const patch = pendingPatch;
    pendingPatch = null;
    const batchWaiters = waiters;
    waiters = [];
    const revisionAtWrite = snapshotRevision;
    let merged: DenAppStateV1;
    try {
      merged = await patchAppState(patch, snapshot);
    } catch (error) {
      pendingPatch = { ...patch, ...(pendingPatch ?? {}) };
      const failed = [...batchWaiters, ...waiters];
      waiters = [];
      for (const waiter of failed) waiter.reject(error);
      throw error;
    }
    if (snapshotRevision === revisionAtWrite) {
      snapshot = merged;
    }
    for (const waiter of batchWaiters) waiter.resolve();
  }
}

function scheduleFlush(): void {
  if (flushing) return;
  retryBlocked = false;
  const now = Date.now();
  if (firstQueuedAt === 0) firstQueuedAt = now;
  clearTimeout(flushTimer);
  const remaining = Math.max(0, APP_STATE_WRITE_MAX_DELAY_MS - (now - firstQueuedAt));
  flushTimer = setTimeout(() => {
    flushTimer = undefined;
    void flushAppState().catch(() => undefined);
  }, Math.min(APP_STATE_WRITE_DEBOUNCE_MS, remaining));
}

export function flushAppState(): Promise<void> {
  clearTimeout(flushTimer);
  flushTimer = undefined;
  firstQueuedAt = 0;
  if (flushing) return flushing;
  flushing = Promise.resolve()
    .then(() => drainPendingPatches())
    .catch((error) => {
      retryBlocked = true;
      throw error;
    })
    .finally(() => {
      flushing = null;
      if (pendingPatch !== null && !retryBlocked) {
        scheduleFlush();
      }
    });
  return flushing;
}
