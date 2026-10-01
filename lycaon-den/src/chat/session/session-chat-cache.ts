import { unwrap } from "solid-js/store";
import type { WorkerTranscript } from "../worker/worker-transcript.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type {
  SessionChatScope,
  SessionChatSnapshot,
} from "./session-chat-snapshot.ts";
import { persistLastSessionSnapshot } from "./session-chat-persist.ts";
import { flushTranscriptRowHeightsToDisk } from "../transcript/layout/transcript-row-heights-persist.ts";
import { materializeTranscriptWindow } from "../transcript/layout/transcript-window.ts";

export const SESSION_CHAT_CACHE_MAX = 8;

const cache = new Map<string, SessionChatSnapshot>();

function scopeKey(scope: SessionChatScope): string {
  return `${scope.projectId}:${scope.sessionId}`;
}

function evictPastCap(): void {
  if (cache.size <= SESSION_CHAT_CACHE_MAX) return;
  const entries = [...cache.entries()].sort(
    (a, b) => a[1].touchedAt - b[1].touchedAt,
  );
  for (const [key] of entries.slice(0, cache.size - SESSION_CHAT_CACHE_MAX)) {
    cache.delete(key);
  }
}

function foregroundScope(appStore: AppStore): SessionChatScope | null {
  const session = appStore.state.currentSession;
  const sessionId = session?.id?.trim();
  const projectId = session?.project_id?.trim();
  if (!sessionId || !projectId) return null;
  return { projectId, sessionId };
}

/** Cloning detaches entries from store proxies. */
function cloneStoreValue<T>(value: T): T {
  return structuredClone(unwrap(value));
}

export function putSessionChatCache(snapshot: SessionChatSnapshot): void {
  const key = scopeKey(snapshot.scope);
  cache.set(key, { ...snapshot, touchedAt: snapshot.touchedAt });
  evictPastCap();
}

export function getSessionChatCache(
  scope: SessionChatScope,
): SessionChatSnapshot | undefined {
  const hit = cache.get(scopeKey(scope));
  if (!hit) return undefined;
  const refreshed = { ...hit, touchedAt: Date.now() };
  cache.set(scopeKey(scope), refreshed);
  return refreshed;
}

export function dropSessionChatCache(scope: SessionChatScope): void {
  cache.delete(scopeKey(scope));
}

export function dropSessionChatCacheForProject(projectId: string): void {
  const pid = projectId.trim();
  if (!pid) return;
  for (const key of cache.keys()) {
    if (key.startsWith(`${pid}:`)) cache.delete(key);
  }
}

export function dropSessionChatCacheForSession(sessionId: string): void {
  const id = sessionId.trim();
  if (!id) return;
  for (const [key, snap] of cache.entries()) {
    if (snap.scope.sessionId === id) cache.delete(key);
  }
}

export function clearSessionChatCacheForTests(): void {
  cache.clear();
}

export function captureSessionChatFromStore(
  appStore: AppStore,
  scope: SessionChatScope,
): SessionChatSnapshot | null {
  const session = appStore.state.currentSession;
  if (!session) return null;
  const sessionId = session.id?.trim();
  const projectId = session.project_id?.trim();
  if (!sessionId || !projectId) return null;
  if (sessionId !== scope.sessionId || projectId !== scope.projectId) return null;
  if (appStore.state.transcriptSessionId?.trim() !== sessionId) return null;

  const workers = appStore.state.workers.filter(
    (w) => w.parent_session_id?.trim() === sessionId,
  );
  const workerIds = new Set(workers.map((w) => w.id));
  const workerTranscripts: Record<string, WorkerTranscript> = {};
  for (const [id, entry] of Object.entries(appStore.state.workerTranscripts)) {
    if (workerIds.has(id)) {
      workerTranscripts[id] = cloneStoreValue(entry);
    }
  }

  const transcript = cloneStoreValue(appStore.state.transcript);
  return {
    scope: { projectId, sessionId },
    touchedAt: Date.now(),
    session: cloneStoreValue(session),
    transcript,
    messages: materializeTranscriptWindow(transcript),
    transcriptWatermark: appStore.state.transcriptWatermark,
    turnClocks: cloneStoreValue(Object.values(appStore.state.turnClocks)),
    turnLoads: cloneStoreValue(Object.values(appStore.state.turnLoads).flat()),
    workers: cloneStoreValue(workers),
    workerTranscripts,
    activeWorkflowRun: appStore.state.activeWorkflowRun
      ? cloneStoreValue(appStore.state.activeWorkflowRun)
      : undefined,
    workflowRuns: cloneStoreValue(appStore.state.workflowRuns),
    workflowCatalog: cloneStoreValue(appStore.state.workflowCatalog),
    blueprints: cloneStoreValue(appStore.state.blueprints),
    pendingCheckpoints: cloneStoreValue(appStore.state.pendingCheckpoints),
  };
}

export function rememberSessionChatFromStore(
  appStore: AppStore,
  scope?: SessionChatScope,
): void {
  const resolved = scope ?? foregroundScope(appStore);
  if (!resolved) return;
  const snap = captureSessionChatFromStore(appStore, resolved);
  if (snap) {
    putSessionChatCache(snap);
    void persistLastSessionSnapshot(snap).catch(() => undefined);
    void flushTranscriptRowHeightsToDisk().catch(() => undefined);
  }
}

/** Install an LRU snapshot so the store names this session. */
export function restoreCachedSessionChat(
  appStore: AppStore,
  scope: SessionChatScope,
): boolean {
  const cached = getSessionChatCache(scope);
  if (!cached) return false;
  appStore.actions.restoreSessionChatSnapshot(cached);
  return appStore.state.currentSession?.id?.trim() === scope.sessionId.trim();
}
