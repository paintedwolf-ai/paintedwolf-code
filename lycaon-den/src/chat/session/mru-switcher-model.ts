import { sessionTitle } from "./session-title.ts";
import type { AttentionRow } from "../../api/types.ts";
import type { RecentSession } from "../../../shared/app-state-types.ts";

/** Switcher entries span projects. */
export type MruEntry = {
  projectId: string;
  sessionId: string;
  title: string;
  projectName?: string;
  attention?: AttentionRow;
};

/** Maximum visible switcher entries. */
export const MRU_SWITCHER_CAP = 8;

function key(projectId: string, sessionId: string): string {
  return `${projectId}:${sessionId}`;
}

/** Ranks chats by local visits; unvisited chats retain prompt-recency order. */
export function createMruTracker() {
  let visited: string[] = [];

  return {
    visit(projectId: string, sessionId: string): void {
      const id = key(projectId, sessionId);
      visited = [id, ...visited.filter((v) => v !== id)];
    },
    forget(projectId: string, sessionId: string): void {
      const id = key(projectId, sessionId);
      visited = visited.filter((v) => v !== id);
    },
    /** Visit order first, then anything unvisited in the order supplied. */
    order<T extends { projectId: string; sessionId: string }>(rows: readonly T[]): T[] {
      const rank = new Map(visited.map((id, i) => [id, i] as const));
      return [...rows].sort((a, b) => {
        const ra = rank.get(key(a.projectId, a.sessionId)) ?? Number.MAX_SAFE_INTEGER;
        const rb = rank.get(key(b.projectId, b.sessionId)) ?? Number.MAX_SAFE_INTEGER;
        return ra - rb;
      });
    },
    /** Test/debug view of the stack. */
    snapshot(): readonly string[] {
      return visited;
    },
  };
}

export type MruTracker = ReturnType<typeof createMruTracker>;

/** Input recents are in prompt-recency order. */
export function buildMruEntries(
  tracker: MruTracker,
  recents: readonly RecentSession[],
  lookup: {
    projectName: (projectId: string) => string | undefined;
    attention: (sessionId: string) => AttentionRow | undefined;
  },
): MruEntry[] {
  return tracker
    .order(recents)
    .slice(0, MRU_SWITCHER_CAP)
    .map((row) => ({
      projectId: row.projectId,
      sessionId: row.sessionId,
      title: sessionTitle(row.title),
      projectName: lookup.projectName(row.projectId),
      attention: lookup.attention(row.sessionId),
    }));
}

/** Forward skips the current chat; backward starts at the oldest entry. */
export function initialMruIndex(count: number, direction: 1 | -1): number {
  if (count <= 0) return 0;
  if (count === 1) return 0;
  return direction === 1 ? 1 : count - 1;
}

/** Wrapping step through the list while the modifier is still held. */
export function nextMruIndex(
  current: number,
  count: number,
  direction: 1 | -1,
): number {
  if (count <= 0) return 0;
  return (current + direction + count) % count;
}
