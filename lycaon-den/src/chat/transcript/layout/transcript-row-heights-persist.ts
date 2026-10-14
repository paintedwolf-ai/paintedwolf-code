import type { SessionChatScope } from "../../session/session-chat-snapshot.ts";
import {
  getAppStateSnapshot,
  persistAppState,
} from "../../../store/app-state-snapshot.ts";
import type { TranscriptRowHeightsSession } from "../../../../shared/app-state-types.ts";
import {
  TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP,
  TRANSCRIPT_ROW_HEIGHTS_SESSION_CAP,
  TRANSCRIPT_ROW_HEIGHT_VARIANT_CAP,
} from "../../../../shared/app-state-types.ts";

export { transcriptRowContentRevision } from "./transcript-row-revision.ts";

/**
 * Measured heights by row. A row remembers the content revision it measured and one height
 * per layout variant (geometry and presentation); a new revision drops every variant, since
 * each measured the old content. Rows and variants are insertion-ordered maps re-inserted on
 * touch, so each is a least-recently-used list: a window resize cycles a row's variants and
 * never evicts another row, and a session keeps its most recently seen rows.
 */
type RowHeights = { revision: string; variants: Map<string, number> };

type SessionRowHeights = {
  scope: SessionChatScope;
  touchedAt: number;
  rows: Map<string, RowHeights>;
};

const persistedByScope = new Map<string, SessionRowHeights>();
let persistTimer: ReturnType<typeof setTimeout> | undefined;

function scopeKey(scope: SessionChatScope): string {
  return `${scope.projectId}:${scope.sessionId}`;
}

function roundHeight(value: number): number {
  return Math.round(value * 64) / 64;
}

function validHeight(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value > 0;
}

/** The row's entry for this revision, most recently used, with stale variants dropped. */
function touchRow(session: SessionRowHeights, itemKey: string, revision: string): RowHeights {
  const existing = session.rows.get(itemKey);
  if (existing) session.rows.delete(itemKey);
  const row = existing?.revision === revision ? existing : { revision, variants: new Map<string, number>() };
  session.rows.set(itemKey, row);
  while (session.rows.size > TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP) {
    const oldest = session.rows.keys().next().value;
    if (oldest === undefined) break;
    session.rows.delete(oldest);
  }
  return row;
}

function setVariant(row: RowHeights, variant: string, height: number): void {
  row.variants.delete(variant);
  row.variants.set(variant, height);
  while (row.variants.size > TRANSCRIPT_ROW_HEIGHT_VARIANT_CAP) {
    const oldest = row.variants.keys().next().value;
    if (oldest === undefined) break;
    row.variants.delete(oldest);
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

function sessionFromPersisted(persisted: TranscriptRowHeightsSession): SessionRowHeights | null {
  const projectId = persisted.projectId.trim();
  const sessionId = persisted.sessionId.trim();
  if (!projectId || !sessionId) return null;
  const session: SessionRowHeights = { scope: { projectId, sessionId }, touchedAt: persisted.touchedAt, rows: new Map() };
  for (const [itemKey, saved] of Object.entries(persisted.rows)) {
    const row = touchRow(session, itemKey, saved.revision);
    for (const [variant, height] of Object.entries(saved.heights)) setVariant(row, variant, height);
  }
  return session.rows.size > 0 ? session : null;
}

function ensurePersistedSession(scope: SessionChatScope): SessionRowHeights {
  const key = scopeKey(scope);
  let session = persistedByScope.get(key);
  if (!session) {
    session = {
      scope,
      touchedAt: Date.now(),
      rows: new Map(),
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
  const sessions = getAppStateSnapshot().transcriptRowHeights;
  if (!sessions?.length) return;
  for (const persisted of sessions) {
    const session = sessionFromPersisted(persisted);
    if (session) persistedByScope.set(scopeKey(session.scope), session);
  }
}

/** Identifies one measurement: the row, the content it showed, and the layout variant. */
export type TranscriptRowHeightKey = { itemKey: string; revision: string; variant: string };

function cleanKey(key: TranscriptRowHeightKey): TranscriptRowHeightKey | null {
  const itemKey = key.itemKey.trim();
  const revision = key.revision.trim();
  const variant = key.variant.trim();
  return itemKey && revision && variant ? { itemKey, revision, variant } : null;
}

export function recordTranscriptRowHeight(
  scope: SessionChatScope,
  key: TranscriptRowHeightKey,
  height: number,
): void {
  const clean = cleanKey(key);
  if (!clean || !validHeight(height)) return;
  const rounded = roundHeight(height);
  const session = ensurePersistedSession(scope);
  session.touchedAt = Date.now();
  const row = touchRow(session, clean.itemKey, clean.revision);
  const unchanged = row.variants.get(clean.variant) === rounded;
  setVariant(row, clean.variant, rounded);
  if (unchanged) return;
  capPersistedSessions();
  schedulePersistToDisk();
}

/** The remembered height for exactly this content and layout, if any. */
export function transcriptRowHeightForScope(
  scope: SessionChatScope,
  key: TranscriptRowHeightKey,
): number | undefined {
  const clean = cleanKey(key);
  if (!clean) return undefined;
  const session = persistedByScope.get(scopeKey(scope));
  const row = session?.rows.get(clean.itemKey);
  if (!session || row?.revision !== clean.revision) return undefined;
  const height = row.variants.get(clean.variant);
  if (height !== undefined) {
    touchRow(session, clean.itemKey, clean.revision);
    setVariant(row, clean.variant, height);
    session.touchedAt = Date.now();
  }
  return height;
}

/** Rows a session remembers. */
export function transcriptRowHeightCountForTests(scope: SessionChatScope): number {
  return persistedByScope.get(scopeKey(scope))?.rows.size ?? 0;
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
      rows: Object.fromEntries(
        [...session.rows].map(([itemKey, row]) => [
          itemKey,
          { revision: row.revision, heights: Object.fromEntries(row.variants) },
        ]),
      ),
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
  rows: Record<string, { revision: string; heights: Record<string, number> }>,
): void {
  const session = ensurePersistedSession(scope);
  for (const [itemKey, saved] of Object.entries(rows)) {
    const row = touchRow(session, itemKey, saved.revision);
    for (const [variant, height] of Object.entries(saved.heights)) {
      if (validHeight(height)) setVariant(row, variant, roundHeight(height));
    }
  }
  session.touchedAt = Date.now();
}

/** Reads one session's saved heights; rows in any other shape are dropped, since this is a rebuildable cache. */
export function parseTranscriptRowHeightsSession(
  raw: unknown,
): TranscriptRowHeightsSession | null {
  if (typeof raw !== "object" || raw === null) return null;
  const session = raw as Partial<TranscriptRowHeightsSession>;
  const projectId = typeof session.projectId === "string" ? session.projectId.trim() : "";
  const sessionId = typeof session.sessionId === "string" ? session.sessionId.trim() : "";
  if (!projectId || !sessionId) return null;
  if (typeof session.touchedAt !== "number" || !Number.isFinite(session.touchedAt)) {
    return null;
  }
  if (typeof session.rows !== "object" || session.rows === null) return null;
  const rows: TranscriptRowHeightsSession["rows"] = {};
  for (const [itemKey, saved] of Object.entries(session.rows as Record<string, unknown>)) {
    if (!itemKey.trim() || typeof saved !== "object" || saved === null) continue;
    const { revision, heights } = saved as { revision?: unknown; heights?: unknown };
    if (typeof revision !== "string" || !revision.trim()) continue;
    if (typeof heights !== "object" || heights === null) continue;
    const kept: Record<string, number> = {};
    for (const [variant, height] of Object.entries(heights)) {
      if (variant.trim() && validHeight(height)) kept[variant] = roundHeight(height);
    }
    if (Object.keys(kept).length > 0) rows[itemKey] = { revision, heights: kept };
  }
  if (Object.keys(rows).length === 0) return null;
  return { projectId, sessionId, touchedAt: session.touchedAt, rows };
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
