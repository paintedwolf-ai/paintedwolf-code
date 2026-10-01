import type {
  Message,
  BlueprintSummary,
  Session,
  TurnClock,
  TurnLoad,
  WorkerTask,
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import type { PendingCheckpoint } from "../checkpoint/checkpoint-model.ts";
import { isRecord } from "../../utils/type-guards.ts";
import { lastSeenStoreRevision } from "../../platform/connection/health.ts";
import {
  getAppStateSnapshot,
  persistAppState,
} from "../../store/app-state-snapshot.ts";
import {
  getSessionChatCache,
  putSessionChatCache,
} from "./session-chat-cache.ts";
import type { SessionChatScope, SessionChatSnapshot } from "./session-chat-snapshot.ts";
import { cachedProjectsFromRegistry } from "../../store/boot-cache-model.ts";
import {
  materializeTranscriptWindow,
  TRANSCRIPT_PAGE_BUDGET,
  TRANSCRIPT_PERSIST_TAIL_LIMIT,
  type TranscriptWindow,
} from "../transcript/layout/transcript-window.ts";

/** Session snapshot stored without derived message rows. */
/** Worker transcripts stay in memory; a restored run hydrates from the host. */
export type PersistedSessionChatSnapshot = Omit<
  SessionChatSnapshot,
  "messages" | "workerTranscripts"
> & {
  storeRevisionAtPersist: number | null;
};

function parseScope(value: unknown): SessionChatScope | null {
  if (!isRecord(value)) return null;
  const projectId =
    typeof value.projectId === "string" ? value.projectId.trim() : "";
  const sessionId =
    typeof value.sessionId === "string" ? value.sessionId.trim() : "";
  if (!projectId || !sessionId) return null;
  return { projectId, sessionId };
}

function parseSession(value: unknown): Session | null {
  if (!isRecord(value)) return null;
  const id = typeof value.id === "string" ? value.id.trim() : "";
  if (!id) return null;
  return value as unknown as Session;
}

function parseTranscript(value: unknown): TranscriptWindow | null {
  if (!isRecord(value)) return null;
  if (!Array.isArray(value.tail)) return null;
  if (!isRecord(value.pages)) return null;
  if (typeof value.hasTailGap !== "boolean") return null;
  if (typeof value.hasMoreBefore !== "boolean") return null;
  if (typeof value.hasMoreAfter !== "boolean") return null;
  const pages: Record<string, Message[]> = {};
  for (const [key, rows] of Object.entries(value.pages)) {
    if (!Array.isArray(rows)) return null;
    pages[key] = rows as Message[];
  }
  return {
    tail: value.tail as Message[],
    pages,
    hasTailGap: value.hasTailGap,
    hasMoreBefore: value.hasMoreBefore,
    hasMoreAfter: value.hasMoreAfter,
  };
}

export function parsePersistedSessionSnapshot(
  raw: unknown,
): PersistedSessionChatSnapshot | null {
  if (!isRecord(raw)) return null;
  const scope = parseScope(raw.scope);
  const session = parseSession(raw.session);
  const transcript = parseTranscript(raw.transcript);
  if (!scope || !session || !transcript) return null;
  if (typeof raw.touchedAt !== "number" || !Number.isFinite(raw.touchedAt)) {
    return null;
  }
  if (
    typeof raw.transcriptWatermark !== "number" ||
    !Number.isFinite(raw.transcriptWatermark)
  ) {
    return null;
  }
  if (
    !Array.isArray(raw.turnClocks) ||
    !Array.isArray(raw.turnLoads) ||
    !Array.isArray(raw.workers) ||
    !Array.isArray(raw.workflowRuns) ||
    !Array.isArray(raw.workflowCatalog) ||
    !Array.isArray(raw.blueprints) ||
    !Array.isArray(raw.pendingCheckpoints)
  ) {
    return null;
  }
  if (raw.activeWorkflowRun !== undefined && !isRecord(raw.activeWorkflowRun)) {
    return null;
  }
  if (!("storeRevisionAtPersist" in raw)) return null;
  const storeRevisionAtPersist =
    raw.storeRevisionAtPersist === null
      ? null
      : typeof raw.storeRevisionAtPersist === "number" &&
          Number.isFinite(raw.storeRevisionAtPersist)
        ? raw.storeRevisionAtPersist
        : undefined;
  if (storeRevisionAtPersist === undefined) return null;
  return {
    scope,
    touchedAt: raw.touchedAt,
    session,
    transcript,
    transcriptWatermark: raw.transcriptWatermark,
    turnClocks: raw.turnClocks as TurnClock[],
    turnLoads: raw.turnLoads as TurnLoad[],
    workers: raw.workers as WorkerTask[],
    activeWorkflowRun: isRecord(raw.activeWorkflowRun)
      ? (raw.activeWorkflowRun as unknown as WorkflowRun)
      : undefined,
    workflowRuns: raw.workflowRuns as WorkflowRun[],
    workflowCatalog: raw.workflowCatalog as WorkflowSummary[],
    blueprints: raw.blueprints as BlueprintSummary[],
    pendingCheckpoints: raw.pendingCheckpoints as PendingCheckpoint[],
    storeRevisionAtPersist,
  };
}

/** Bound the persisted window and omit worker transcripts. */
export function trimSnapshotForPersist(
  snap: SessionChatSnapshot,
): PersistedSessionChatSnapshot {
  const pages = { ...snap.transcript.pages };
  const tailFirstPages = Object.entries(pages).sort(
    (a, b) => (b[1][0]?.ord ?? 0) - (a[1][0]?.ord ?? 0),
  );
  let trimmedPages = false;
  while (tailFirstPages.length > TRANSCRIPT_PAGE_BUDGET) {
    const evict = tailFirstPages.shift();
    if (evict) {
      delete pages[evict[0]];
      trimmedPages = true;
    }
  }
  // Persist enough tail rows for the first restored viewport.
  const tail = snap.transcript.tail.slice(-TRANSCRIPT_PERSIST_TAIL_LIMIT);
  const trimmedTail = tail.length < snap.transcript.tail.length;
  const transcript: TranscriptWindow = {
    tail,
    pages,
    hasTailGap:
      snap.transcript.hasTailGap ||
      trimmedPages ||
      (trimmedTail && Object.keys(pages).length > 0),
    // A trimmed tail always has older history.
    hasMoreBefore: snap.transcript.hasMoreBefore || trimmedTail,
    hasMoreAfter: snap.transcript.hasMoreAfter,
  };
  const { messages: _derived, workerTranscripts: _memoryOnly, ...rest } = snap;
  return {
    ...rest,
    transcript,
    storeRevisionAtPersist: lastSeenStoreRevision(),
  };
}

export function toSessionChatSnapshot(
  persisted: PersistedSessionChatSnapshot,
): SessionChatSnapshot {
  const { storeRevisionAtPersist: _, ...rest } = persisted;
  return { ...rest, messages: materializeTranscriptWindow(rest.transcript), workerTranscripts: {} };
}

export function persistedSnapshotMatchesRevision(
  revision: number | null,
): boolean {
  const persisted = parsePersistedSessionSnapshot(
    getAppStateSnapshot().lastSessionSnapshot,
  );
  if (!persisted) return false;
  if (persisted.storeRevisionAtPersist === null) return revision === null;
  if (revision === null) return true;
  return persisted.storeRevisionAtPersist === revision;
}

export async function persistLastSessionSnapshot(
  snap: SessionChatSnapshot,
): Promise<void> {
  await persistAppState({
    lastSessionSnapshot: trimSnapshotForPersist(snap),
  });
}

export async function clearPersistedLastSessionSnapshot(): Promise<void> {
  await persistAppState({ lastSessionSnapshot: undefined });
}

/** Drops the cold-boot snapshot only when it belongs to the retired chat. */
export async function forgetPersistedLastSessionSnapshot(scope: SessionChatScope): Promise<void> {
  const raw = getAppStateSnapshot().lastSessionSnapshot;
  const saved = isRecord(raw) ? parseScope(raw.scope) : null;
  if (saved?.projectId !== scope.projectId || saved.sessionId !== scope.sessionId) return;
  await clearPersistedLastSessionSnapshot();
}

export function seedSessionChatCacheFromPersisted(): SessionChatScope | null {
  const persisted = parsePersistedSessionSnapshot(
    getAppStateSnapshot().lastSessionSnapshot,
  );
  if (!persisted) return null;
  if (getSessionChatCache(persisted.scope)) return persisted.scope;
  putSessionChatCache(toSessionChatSnapshot(persisted));
  return persisted.scope;
}

export async function syncCachedProjectsToDisk(
  projects: readonly import("../../api/types.ts").Project[],
): Promise<void> {
  await persistAppState({
    cachedProjects: cachedProjectsFromRegistry(projects),
  });
}
