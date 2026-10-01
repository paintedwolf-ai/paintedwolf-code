import { createStore } from "solid-js/store";
import {
  DEFAULT_SESSION_TITLE,
  RECENTS_CAP,
  type RecentSession,
  type RecentSessionInput,
} from "../../shared/app-state-types.ts";
import {
  loadSharedAppState,
  persistAppState,
} from "./app-state-snapshot.ts";

export type RecentsStoreState = {
  recents: RecentSession[];
  loaded: boolean;
};

function resolveTitle(title: string | undefined): string {
  return title?.trim() || DEFAULT_SESSION_TITLE;
}

function resolveProjectId(entry: RecentSessionInput): string {
  return entry.projectId.trim();
}

function findRecentIndex(
  recents: readonly RecentSession[],
  projectId: string,
  sessionId: string,
): number {
  return recents.findIndex(
    (r) => r.projectId === projectId && r.sessionId === sessionId,
  );
}

/** Add or update a session row without changing list order or activity time. */
export function registerRecent(
  recents: RecentSession[],
  entry: RecentSessionInput,
): RecentSession[] {
  const title = resolveTitle(entry.title);
  const projectId = resolveProjectId(entry);
  const idx = findRecentIndex(recents, projectId, entry.sessionId);
  if (idx >= 0) {
    const existing = recents[idx]!;
    if (existing.title === title) return recents;
    return recents.map((row, i) => (i === idx ? { ...row, title } : row));
  }
  // Keep the newly opened session when trimming the oldest entry.
  return [
    ...recents.slice(0, RECENTS_CAP - 1),
    {
      projectId,
      sessionId: entry.sessionId,
      title,
    },
  ];
}

/** Returns the next same-project session to foreground after removal. */
export function nextRecentSession(
  recents: readonly RecentSession[],
  removed: { projectId: string; sessionId: string },
): RecentSession | null {
  return (
    recents.find(
      (r) =>
        r.projectId === removed.projectId && r.sessionId !== removed.sessionId,
    ) ?? null
  );
}

/** Promote a session to the front on the user's own prompt (or its creation),
 *  stamping the time. Recents order tracks the user's last prompt, not views or agent work. */
export function recordRecentActivity(
  recents: RecentSession[],
  entry: RecentSessionInput,
): RecentSession[] {
  const now = Date.now();
  const projectId = resolveProjectId(entry);
  const idx = findRecentIndex(recents, projectId, entry.sessionId);
  const title =
    entry.title != null
      ? resolveTitle(entry.title)
      : idx >= 0
        ? recents[idx]!.title
        : DEFAULT_SESSION_TITLE;
  const row: RecentSession =
    idx >= 0
      ? { ...recents[idx]!, title, lastActivityAt: now }
      : {
          projectId,
          sessionId: entry.sessionId,
          title,
          lastActivityAt: now,
        };
  const without = recents.filter(
    (r) =>
      !(
        r.projectId === projectId && r.sessionId === entry.sessionId
      ),
  );
  return [row, ...without].slice(0, RECENTS_CAP);
}

/** Remove one recent session row (pure). */
export function filterOutRecent(
  recents: RecentSession[],
  projectId: string,
  sessionId: string,
): RecentSession[] {
  return recents.filter(
    (r) => !(r.projectId === projectId && r.sessionId === sessionId),
  );
}

/** Remove all recent sessions for a project (pure). */
export function filterOutProjectRecents(
  recents: RecentSession[],
  projectId: string,
): RecentSession[] {
  return recents.filter((r) => r.projectId !== projectId);
}

export function createRecentsStore() {
  const [state, setState] = createStore<RecentsStoreState>({
    recents: [],
    loaded: false,
  });

  const api = {
    state,
    async load() {
      const doc = await loadSharedAppState();
      setState({ recents: doc.recents, loaded: true });
    },
    async registerSession(entry: RecentSessionInput) {
      setState("recents", registerRecent(state.recents, entry));
      await persistAppState({ recents: state.recents });
    },
    async recordSessionActivity(entry: RecentSessionInput) {
      setState("recents", recordRecentActivity(state.recents, entry));
      await persistAppState({ recents: state.recents });
    },
    async removeRecent(input: { projectId: string; sessionId: string }) {
      const recents = filterOutRecent(
        state.recents,
        input.projectId,
        input.sessionId,
      );
      setState("recents", recents);
      await persistAppState({ recents });
    },
    async replaceRecents(recents: RecentSession[]) {
      setState("recents", recents);
      await persistAppState({ recents });
    },
    async removeRecentsForProject(projectId: string) {
      const recents = filterOutProjectRecents(state.recents, projectId);
      setState("recents", recents);
      await persistAppState({ recents });
    },
  };

  return api;
}

export type RecentsStore = ReturnType<typeof createRecentsStore>;
