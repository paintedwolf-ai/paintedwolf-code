import type { SessionChatScope } from "../../session/session-chat-snapshot.ts";
import {
  getAppStateSnapshot,
  persistAppState,
} from "../../../store/app-state-snapshot.ts";
import type { TranscriptRowHeightsSession } from "../../../../shared/app-state-types.ts";
import {
  TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP,
  TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP,
} from "../../../../shared/app-state-types.ts";

type RowHeightEntry = {
  itemKey: string;
  presentation: string;
  height: number;
  touchedAt: number;
};

type SessionRowHeights = {
  scope: SessionChatScope;
  touchedAt: number;
  heights: Map<string, RowHeightEntry>;
};

const persistedByScope = new Map<string, SessionRowHeights>();
let persistTimer: ReturnType<typeof setTimeout> | undefined;

function scopeKey(scope: SessionChatScope): string {
  return `${scope.projectId}:${scope.sessionId}`;
}

function rowLayoutKey(itemKey: string, presentation: string): string {
  return JSON.stringify([itemKey, presentation]);
}

function capSessionHeights(session: SessionRowHeights): void {
  while (session.heights.size > TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP) {
    const oldest = session.heights.keys().next().value;
    if (oldest === undefined) break;
    session.heights.delete(oldest);
  }
}

function capPersistedSessions(): void {
  if (persistedByScope.size <= TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP) return;
  const ranked = [...persistedByScope.values()].sort(
    (a, b) => a.touchedAt - b.touchedAt,
  );
  const drop = ranked.slice(
    0,
    persistedByScope.size - TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP,
  );
  for (const session of drop) {
    persistedByScope.delete(scopeKey(session.scope));
  }
}

function sessionFromPersisted(row: TranscriptRowHeightsSession): SessionRowHeights | null {
  const projectId = row.projectId.trim();
  const sessionId = row.sessionId.trim();
  if (!projectId || !sessionId) return null;
  const heights = new Map<string, RowHeightEntry>();
  for (const [itemKey, presentations] of Object.entries(row.rows)) {
    if (!itemKey) continue;
    for (const [presentation, value] of Object.entries(presentations)) {
      if (!presentation || (!Number.isFinite(value) || !(value > 0))) continue;
      heights.set(rowLayoutKey(itemKey, presentation), {
        itemKey,
        presentation,
        height: Math.round(value * 64) / 64,
        touchedAt: row.touchedAt,
      });
    }
  }
  if (heights.size === 0) return null;
  return {
    scope: { projectId, sessionId },
    touchedAt: row.touchedAt,
    heights,
  };
}

function ensurePersistedSession(scope: SessionChatScope): SessionRowHeights {
  const key = scopeKey(scope);
  let session = persistedByScope.get(key);
  if (!session) {
    session = {
      scope,
      touchedAt: Date.now(),
      heights: new Map(),
    };
    persistedByScope.set(key, session);
  }
  return session;
}

function schedulePersistToDisk(): void {
  clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    void flushTranscriptRowHeightsToDisk().catch(() => undefined);
  }, 800);
}

export function loadTranscriptRowHeightsFromSnapshot(): void {
  persistedByScope.clear();
  const rows = getAppStateSnapshot().transcriptRowHeights;
  if (!rows?.length) return;
  for (const row of rows) {
    const session = sessionFromPersisted(row);
    if (!session) continue;
    persistedByScope.set(scopeKey(session.scope), session);
  }
}

export function recordTranscriptRowHeight(
  scope: SessionChatScope,
  itemKey: string,
  presentation: string,
  height: number,
): void {
  const key = itemKey.trim();
  const state = presentation.trim();
  if (!key || !state || (!Number.isFinite(height) || !(height > 0))) return;

  const rounded = Math.round(height * 64) / 64;
  const now = Date.now();
  const session = ensurePersistedSession(scope);
  const layoutKey = rowLayoutKey(key, state);
  const existing = session.heights.get(layoutKey);
  if (existing) {
    session.heights.delete(layoutKey);
    session.heights.set(layoutKey, existing);
  }
  if (existing?.height === rounded) {
    existing.touchedAt = now;
    session.touchedAt = now;
    return;
  }
  session.heights.set(layoutKey, {
    itemKey: key,
    presentation: state,
    height: rounded,
    touchedAt: now,
  });
  session.touchedAt = now;
  capSessionHeights(session);
  capPersistedSessions();
  schedulePersistToDisk();
}

export function transcriptRowHeightForScope(
  scope: SessionChatScope,
  itemKey: string,
  presentation: string,
): number | undefined {
  const key = itemKey.trim();
  const state = presentation.trim();
  if (!key || !state) return undefined;
  return persistedByScope
    .get(scopeKey(scope))
    ?.heights.get(rowLayoutKey(key, state))?.height;
}

export async function flushTranscriptRowHeightsToDisk(): Promise<void> {
  capPersistedSessions();
  const transcriptRowHeights: TranscriptRowHeightsSession[] = [
    ...persistedByScope.values(),
  ]
    .sort((a, b) => b.touchedAt - a.touchedAt)
    .slice(0, TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP)
    .map((session) => ({
      projectId: session.scope.projectId,
      sessionId: session.scope.sessionId,
      touchedAt: session.touchedAt,
      rows: [...session.heights.values()].reduce<
        Record<string, Record<string, number>>
      >((rows, entry) => {
        const presentations = rows[entry.itemKey] ?? {};
        presentations[entry.presentation] = entry.height;
        rows[entry.itemKey] = presentations;
        return rows;
      }, {}),
    }));
  await persistAppState({
    transcriptRowHeights:
      transcriptRowHeights.length > 0 ? transcriptRowHeights : undefined,
  });
}

export function dropPersistedTranscriptRowHeights(scope: SessionChatScope): void {
  const key = scopeKey(scope);
  persistedByScope.delete(key);
  void flushTranscriptRowHeightsToDisk().catch(() => undefined);
}

export function clearTranscriptRowHeightsForTests(): void {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  persistedByScope.clear();
}

export function seedTranscriptRowHeightsForTests(
  scope: SessionChatScope,
  rows: Record<string, Record<string, number>>,
): void {
  const session = ensurePersistedSession(scope);
  const now = Date.now();
  for (const [itemKey, presentations] of Object.entries(rows)) {
    if (!itemKey) continue;
    for (const [presentation, value] of Object.entries(presentations)) {
      if (!presentation || (!Number.isFinite(value) || !(value > 0))) continue;
      session.heights.set(rowLayoutKey(itemKey, presentation), {
        itemKey,
        presentation,
        height: Math.round(value * 64) / 64,
        touchedAt: now,
      });
    }
  }
  session.touchedAt = now;
}

export function parseTranscriptRowHeightsSession(
  raw: unknown,
): TranscriptRowHeightsSession | null {
  if (typeof raw !== "object" || raw === null) return null;
  const row = raw as Partial<TranscriptRowHeightsSession>;
  const projectId = typeof row.projectId === "string" ? row.projectId.trim() : "";
  const sessionId = typeof row.sessionId === "string" ? row.sessionId.trim() : "";
  if (!projectId || !sessionId) return null;
  if (typeof row.touchedAt !== "number" || !Number.isFinite(row.touchedAt)) {
    return null;
  }
  if (typeof row.rows !== "object" || row.rows === null) return null;
  const rows: Record<string, Record<string, number>> = {};
  for (const [itemKey, rawPresentations] of Object.entries(row.rows)) {
    if (
      !itemKey ||
      typeof rawPresentations !== "object" ||
      rawPresentations === null
    ) {
      continue;
    }
    const presentations: Record<string, number> = {};
    for (const [presentation, value] of Object.entries(rawPresentations)) {
      if (!presentation || typeof value !== "number" || (!Number.isFinite(value) || !(value > 0))) continue;
      presentations[presentation] = Math.round(value * 64) / 64;
    }
    if (Object.keys(presentations).length > 0) rows[itemKey] = presentations;
  }
  if (Object.keys(rows).length === 0) return null;
  return { projectId, sessionId, touchedAt: row.touchedAt, rows };
}

export function parseTranscriptRowHeightsSessions(
  raw: unknown,
): TranscriptRowHeightsSession[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((row) => {
    const parsed = parseTranscriptRowHeightsSession(row);
    return parsed ? [parsed] : [];
  });
}
