import { sessionTitle } from "../chat/session/session-title.ts";
import type { SessionSummary } from "../api/types.ts";
import type { ChatListSort } from "../../shared/app-state-types.ts";

/** Chats beyond the pinned group; the full inventory lives in All chats. */
export const SIDEBAR_CHATS_CAP = 7;

/** One sidebar chat row derived from a host SessionSummary. */
export type SidebarChatRow = {
  projectId: string;
  sessionId: string;
  title: string;
  activityAtMs: number;
  /** Order among the project's pinned chats; null when unpinned. */
  pinRank: number | null;
  messageCount: number;
};

/** The sidebar's two groups, each in presentation order. */
export type SidebarChatSections = {
  pinned: SidebarChatRow[];
  chats: SidebarChatRow[];
};

function timeMs(iso: string | undefined): number {
  if (!iso) return 0;
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? 0 : ms;
}

/** SQLite's lower() folds ASCII only; titles order the way the host pages them. */
function hostTitleKey(title: string | undefined): string {
  return (title ?? "").replace(/[A-Z]/g, (c) => c.toLowerCase());
}

function compareText(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** The host's list order for one sort key, id breaking ties in the same direction. */
export function compareChatsForSort(sort: ChatListSort): (a: SessionSummary, b: SessionSummary) => number {
  switch (sort) {
    case "title":
      return (a, b) =>
        compareText(hostTitleKey(a.title), hostTitleKey(b.title)) || compareText(a.id, b.id);
    // Wire times trim trailing zeros, so they compare as instants, not text.
    case "activity":
      return (a, b) =>
        timeMs(b.activity_at) - timeMs(a.activity_at) || compareText(b.id, a.id);
    case "created":
      return (a, b) =>
        timeMs(b.created_at) - timeMs(a.created_at) || compareText(b.id, a.id);
  }
}

/** Pinned chats in pin order, lowest rank first. */
export function comparePinned(a: SessionSummary, b: SessionSummary): number {
  return (a.pin_rank ?? 0) - (b.pin_rank ?? 0) || compareText(a.id, b.id);
}

export function sidebarChatRow(s: SessionSummary): SidebarChatRow {
  return {
    projectId: s.project_id,
    sessionId: s.id,
    title: sessionTitle(s.title),
    activityAtMs: timeMs(s.activity_at),
    pinRank: s.pin_rank ?? null,
    messageCount: s.message_count,
  };
}

/**
 * Every pinned chat, then the first unpinned chats in the chosen order. A
 * selected chat that falls outside both is listed last, so the selection stays
 * visible without moving the rows above it.
 */
export function sidebarChatSections(
  summaries: readonly SessionSummary[],
  sort: ChatListSort,
  selected?: SessionSummary | null,
  cap = SIDEBAR_CHATS_CAP,
): SidebarChatSections {
  const pinned = summaries.filter((s) => s.pin_rank != null).sort(comparePinned);
  const chats = summaries
    .filter((s) => s.pin_rank == null)
    .sort(compareChatsForSort(sort))
    .slice(0, cap);
  const listed = (id: string) => pinned.some((s) => s.id === id) || chats.some((s) => s.id === id);
  if (selected && !listed(selected.id)) {
    (selected.pin_rank != null ? pinned : chats).push(selected);
  }
  return { pinned: pinned.map(sidebarChatRow), chats: chats.map(sidebarChatRow) };
}

/** Rows in the order the sidebar shows them, for keyboard cycling. */
export function sidebarNavigationOrder(sections: SidebarChatSections): SidebarChatRow[] {
  return [...sections.pinned, ...sections.chats];
}

/**
 * Adjacent session in the sidebar's order. Wraps. Returns null when there is
 * nowhere to move (empty or single entry).
 */
export function adjacentSession<T extends { sessionId: string }>(
  sessions: readonly T[],
  currentSessionId: string | null | undefined,
  dir: 1 | -1,
): T | null {
  if (sessions.length <= 1) return null;
  const idx = sessions.findIndex((row) => row.sessionId === currentSessionId);
  if (idx < 0) return sessions[0] ?? null;
  const next = (idx + dir + sessions.length) % sessions.length;
  return sessions[next] ?? null;
}
