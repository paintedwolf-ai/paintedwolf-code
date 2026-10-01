import type { LycaonClient } from "../../api/client.ts";
import type { SessionSummary } from "../../api/types.ts";
import {
  DEFAULT_SESSION_TITLE,
  type RecentSession,
} from "../../../shared/app-state-types.ts";
import { nextRecentSession, type RecentsStore } from "../../store/recents-store.ts";

export type SessionLifecycleTarget = {
  projectId: string;
  sessionId: string;
  title?: string;
};

export type SessionRetirementDeps = {
  client: () => LycaonClient | null;
  reportError: (error: unknown, target: SessionLifecycleTarget) => void;
  removeProjectRow: (sessionId: string) => void;
  /** Shows the edits on the sidebar rows until the host answers `send`. */
  applyProjectRowPatches: (
    patches: ReadonlyMap<string, Partial<SessionSummary>>,
    send: () => Promise<unknown>,
  ) => Promise<void>;
  /** The rank a pin appended now would take. */
  nextPinRank: () => number;
  /** Pinned chats in pin order, as shown. */
  pinnedIds: () => string[];
  refreshProjectRows: () => void;
  retireLocally: (target: SessionLifecycleTarget) => Promise<void>;
};

export async function setSessionArchived(
  deps: SessionRetirementDeps,
  target: SessionLifecycleTarget,
  archived: boolean,
): Promise<boolean> {
  const client = deps.client();
  if (!client) return false;
  try {
    await client.updateSession(target.sessionId, { archived });
  } catch (error) {
    deps.reportError(error, target);
    return false;
  }
  if (archived) deps.removeProjectRow(target.sessionId);
  deps.refreshProjectRows();
  if (archived) await deps.retireLocally(target);
  return true;
}

export async function deleteSession(
  deps: SessionRetirementDeps,
  target: SessionLifecycleTarget,
): Promise<boolean> {
  const client = deps.client();
  if (!client) return false;
  try {
    await client.deleteSession(target.sessionId);
  } catch (error) {
    deps.reportError(error, target);
    return false;
  }
  deps.removeProjectRow(target.sessionId);
  deps.refreshProjectRows();
  await deps.retireLocally(target);
  return true;
}

export async function setSessionPinned(
  deps: SessionRetirementDeps,
  target: SessionLifecycleTarget,
  pinned: boolean,
): Promise<boolean> {
  const client = deps.client();
  if (!client) return false;
  // Pinning appends, so the provisional rank is the one the host will assign.
  const shown = { pin_rank: pinned ? deps.nextPinRank() : undefined };
  try {
    await deps.applyProjectRowPatches(new Map([[target.sessionId, shown]]), () =>
      client.updateSession(target.sessionId, { pinned }),
    );
  } catch (error) {
    deps.reportError(error, target);
    return false;
  }
  deps.refreshProjectRows();
  return true;
}

/** Places a pinned chat at a 1-based position; the host renumbers pins 1…n. */
export async function moveSessionPin(
  deps: SessionRetirementDeps,
  target: SessionLifecycleTarget,
  position: number,
): Promise<boolean> {
  const client = deps.client();
  if (!client) return false;
  const current = deps.pinnedIds();
  if (!current.includes(target.sessionId)) return false;
  const order = current.filter((id) => id !== target.sessionId);
  order.splice(Math.max(0, Math.min(position - 1, order.length)), 0, target.sessionId);
  const shown = new Map(order.map((id, index) => [id, { pin_rank: index + 1 }]));
  try {
    await deps.applyProjectRowPatches(shown, () =>
      client.updateSession(target.sessionId, { pin_position: position }),
    );
  } catch (error) {
    deps.reportError(error, target);
    return false;
  }
  deps.refreshProjectRows();
  return true;
}

export type LocalRetirementDeps = {
  /** The selected chat in this window. */
  activeChat: () => { projectId: string; sessionId: string } | null;
  recents: Pick<RecentsStore, "state">;
  /** Forgets everything Den keeps for the chat, including its recents row. */
  retireEntity: (scope: SessionLifecycleTarget) => void;
  resumeSession: (next: RecentSession, options: { keepStage: boolean }) => Promise<void>;
  evictConversation: (projectId: string, sessionId: string) => void;
};

/** Forget a retired chat; an open one hands off to the next recent chat without
 *  waiting for the recents write. */
export async function retireSessionLocally(
  deps: LocalRetirementDeps,
  target: SessionLifecycleTarget,
): Promise<void> {
  const row: RecentSession = {
    ...target,
    title: target.title || DEFAULT_SESSION_TITLE,
  };
  const active = deps.activeChat();
  const removingActive =
    active?.projectId === row.projectId && active.sessionId === row.sessionId;
  const next = removingActive
    ? nextRecentSession(deps.recents.state.recents, row)
    : null;
  deps.retireEntity(row);
  if (!removingActive) return;
  if (next) {
    // Retiring a subject does not dismiss the project's management view.
    await deps.resumeSession(next, { keepStage: true });
  } else {
    deps.evictConversation(row.projectId, row.sessionId);
  }
}
