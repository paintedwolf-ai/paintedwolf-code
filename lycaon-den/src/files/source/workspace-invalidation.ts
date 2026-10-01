import type { LycaonClient } from "../../api/client.ts";

type Observer = { projectId: string; sessionId: () => string | undefined; refresh: () => void };
const observers = new WeakMap<LycaonClient, Set<Observer>>();

/** The chat is read when an invalidation arrives, so following another chat never re-registers. */
export function observeWorkspaceInvalidation(
  client: LycaonClient,
  projectId: string,
  sessionId: () => string | undefined,
  refresh: () => void,
): () => void {
  let entries = observers.get(client);
  if (!entries) {
    entries = new Set();
    observers.set(client, entries);
  }
  const observer = { projectId, sessionId, refresh };
  entries.add(observer);
  return () => { entries.delete(observer); };
}

/** Omit the chat to refresh every mounted checkout after a shared-folder mutation. */
export function invalidateWorkspace(
  client: LycaonClient,
  projectId: string,
  sessionId?: string,
): void {
  for (const observer of observers.get(client) ?? []) {
    if (observer.projectId === projectId && (sessionId === undefined || observer.sessionId() === sessionId)) observer.refresh();
  }
}
