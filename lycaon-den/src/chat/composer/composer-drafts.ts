import { createSignal } from "solid-js";
import { COMPOSER_DRAFTS_SESSION_CAP } from "../../../shared/app-state-types.ts";
import {
  flushAppState,
  getAppStateSnapshot,
  persistAppState,
} from "../../store/app-state-snapshot.ts";

const DRAFT_PERSIST_DEBOUNCE_MS = 300;

const [drafts, setDrafts] = createSignal<Record<string, string>>({});
let persistTimer: ReturnType<typeof setTimeout> | undefined;

function normalizeDrafts(
  raw: Record<string, string> | undefined,
): Record<string, string> {
  if (!raw) return {};
  const next: Record<string, string> = {};
  for (const [sessionId, text] of Object.entries(raw)) {
    const id = sessionId.trim();
    if (!id || typeof text !== "string" || text.length === 0) continue;
    next[id] = text;
  }
  return capDrafts(next);
}

function capDrafts(map: Record<string, string>): Record<string, string> {
  const keys = Object.keys(map);
  if (keys.length <= COMPOSER_DRAFTS_SESSION_CAP) return map;
  // Drop oldest insertion order first (plain object key order).
  const drop = keys.slice(0, keys.length - COMPOSER_DRAFTS_SESSION_CAP);
  const next = { ...map };
  for (const key of drop) delete next[key];
  return next;
}

function schedulePersistToDisk(): void {
  clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    // Explicit checkpoints flush this queued snapshot to disk.
    void persistComposerDraftSnapshot().catch(() => undefined);
  }, DRAFT_PERSIST_DEBOUNCE_MS);
}

function persistComposerDraftSnapshot(): Promise<void> {
  const current = drafts();
  const keys = Object.keys(current);
  return persistAppState({
    composerDrafts: keys.length > 0 ? current : undefined,
  });
}

/** Reactive unsent composer text for a session (empty string when none). */
export function composerDraftForSession(
  sessionId: string | null | undefined,
): string {
  const id = sessionId?.trim();
  if (!id) return "";
  return drafts()[id] ?? "";
}

/** True when the session has non-whitespace unsent composer text. */
export function hasComposerDraft(sessionId: string | null | undefined): boolean {
  return composerDraftForSession(sessionId).trim().length > 0;
}

/** Write-through draft update; empty text removes the session entry. */
export function setComposerDraft(
  sessionId: string | null | undefined,
  text: string,
): void {
  const id = sessionId?.trim();
  if (!id) return;
  setDrafts((prev) => {
    const next = { ...prev };
    if (text.length === 0) {
      delete next[id];
    } else {
      next[id] = text;
    }
    return capDrafts(next);
  });
  schedulePersistToDisk();
}

/** Replace one live draft from the host mirror without echoing a persistence write. */
export function replaceComposerDraftFromShared(
  sessionId: string,
  text: string,
): void {
  const id = sessionId.trim();
  if (!id) return;
  setDrafts((previous) => {
    const next = { ...previous };
    if (text.length === 0) delete next[id];
    else next[id] = text;
    return capDrafts(next);
  });
}

/** Clear the draft for a session (human send). */
export function clearComposerDraft(sessionId: string | null | undefined): void {
  setComposerDraft(sessionId, "");
}

/** Drop unsent composer text for these sessions. */
export function clearComposerDrafts(sessionIds: readonly string[]): void {
  for (const id of sessionIds) clearComposerDraft(id);
}

export function syncComposerDraftsFromSnapshot(): void {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  setDrafts(normalizeDrafts(getAppStateSnapshot().composerDrafts));
}

export async function flushComposerDraftsToDisk(): Promise<void> {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  const persisted = persistComposerDraftSnapshot();
  await flushAppState();
  await persisted;
}

/** Reset module state between tests. */
export function resetComposerDraftsForTests(): void {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  setDrafts({});
}
