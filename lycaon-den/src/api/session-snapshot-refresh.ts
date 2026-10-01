import type { AppStore } from "../store/app-state-model.ts";
import type { LycaonClient } from "./client.ts";

const readsByContext = new WeakMap<object, Map<string, object>>();

function pendingReads(context: object): Map<string, object> {
  let reads = readsByContext.get(context);
  if (!reads) {
    reads = new Map();
    readsByContext.set(context, reads);
  }
  return reads;
}

export function resetSessionSnapshotReads(context: object): void {
  readsByContext.delete(context);
}

export function sessionSnapshotReadFence(context: object) {
  const reads = pendingReads(context);
  return {
    invalidate: (sessionId: string): void => { reads.delete(sessionId); },
    clear: (): void => { reads.clear(); },
  };
}

export function beginSessionSnapshotRead(appStore: AppStore, sessionId: string) {
  const context = appStore.actions;
  const sessionViewEpoch = appStore.state.sessionViewEpoch;
  const activity = appStore.state.sessionActivity[sessionId];
  const reads = pendingReads(context);
  const request = {};
  reads.set(sessionId, request);
  const isUnchanged = () =>
    readsByContext.get(context) === reads &&
    reads.get(sessionId) === request &&
    appStore.state.sessionActivity[sessionId] === activity;
  return {
    isSameBackend: () => readsByContext.get(context) === reads,
    isUnchanged,
    isCurrent: () =>
      isUnchanged() &&
      appStore.state.sessionViewEpoch === sessionViewEpoch &&
      appStore.state.currentSession?.id === sessionId,
    finish: () => {
      if (reads.get(sessionId) === request) reads.delete(sessionId);
    },
  };
}

/** A newer request or accepted event supersedes an in-flight snapshot read. */
export async function refreshSessionSnapshot(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  shouldApply: () => boolean = () => true,
): Promise<void> {
  const read = beginSessionSnapshotRead(appStore, sessionId);
  try {
    const session = await client.getSession(sessionId);
    if (read.isCurrent() && shouldApply()) appStore.actions.mergeSession(session);
  } finally {
    read.finish();
  }
}
