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

const PRESENTATIONS_PER_ROW = 4;

type SessionRowHeights = {
  scope: SessionChatScope;
  touchedAt: number;
  heights: Map<string, Map<string, number>>;
};

const persistedByScope = new Map<string, SessionRowHeights>();
let persistTimer: ReturnType<typeof setTimeout> | undefined;

function scopeKey(scope: SessionChatScope): string {
  return `${scope.projectId}:${scope.sessionId}`;
}

function capSessionHeights(session: SessionRowHeights): void {
  while (session.heights.size > TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP) {
    const oldest = session.heights.keys().next().value;
    if (oldest === undefined) break;
    session.heights.delete(oldest);
  }
}

function storeHeight(session: SessionRowHeights, item: string, presentation: string, height: number): boolean {
  const variants = session.heights.get(item) ?? new Map<string, number>();
  const changed = variants.get(presentation) !== height;
  variants.delete(presentation);
  variants.set(presentation, height);
  while (variants.size > PRESENTATIONS_PER_ROW) variants.delete(variants.keys().next().value!);
  session.heights.delete(item);
  session.heights.set(item, variants);
  capSessionHeights(session);
  return changed;
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
  const session: SessionRowHeights = { scope: { projectId, sessionId }, touchedAt: row.touchedAt, heights: new Map() };
  for (const [itemKey, presentations] of Object.entries(row.rows)) {
    if (!itemKey) continue;
    for (const [presentation, value] of Object.entries(presentations)) {
      if (!presentation || !Number.isFinite(value) || !(value > 0)) continue;
      storeHeight(session, itemKey, presentation, Math.round(value * 64) / 64);
    }
  }
  return session.heights.size ? session : null;
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
  const changed = storeHeight(session, key, state, rounded);
  session.touchedAt = now;
  capPersistedSessions();
  if (changed) schedulePersistToDisk();
}

export function transcriptRowHeightForScope(
  scope: SessionChatScope,
  itemKey: string,
  presentation: string,
): number | undefined {
  const key = itemKey.trim();
  const state = presentation.trim();
  if (!key || !state) return undefined;
  const session = persistedByScope.get(scopeKey(scope));
  const variants = session?.heights.get(key);
  const height = variants?.get(state);
  if (height !== undefined && session) {
    storeHeight(session, key, state, height);
    session.touchedAt = Date.now();
  }
  return height;
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
      rows: Object.fromEntries([...session.heights].map(([item, variants]) => [item, Object.fromEntries(variants)])),
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
      storeHeight(session, itemKey, presentation, Math.round(value * 64) / 64);
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
