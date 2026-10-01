import type { LycaonClient } from "../../api/client.ts";
import type { Project } from "../../api/types.ts";
import type { RecentSession } from "../../../shared/app-state-types.ts";
import {
  getSessionChatCache,
  putSessionChatCache,
} from "./session-chat-cache.ts";
import type { SessionChatScope } from "./session-chat-snapshot.ts";
import { isSessionNotFoundError } from "./session-not-found.ts";

export const PREFETCH_RECENT_PROJECTS = 4;

/** Distinct project ids in recents activity order. */
export function prefetchProjectIds(
  recents: readonly RecentSession[],
  cap = PREFETCH_RECENT_PROJECTS,
): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const row of recents) {
    const id = row.projectId.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push(id);
    if (out.length >= cap) break;
  }
  return out;
}

/** First recents row per project id (activity order). */
export function firstRecentSessionByProject(
  recents: readonly RecentSession[],
  projectIds: readonly string[],
): SessionChatScope[] {
  const targets: SessionChatScope[] = [];
  for (const projectId of projectIds) {
    const row = recents.find((r) => r.projectId === projectId);
    if (!row) continue;
    targets.push({ projectId: row.projectId, sessionId: row.sessionId });
  }
  return targets;
}

/** Warm the in-memory session LRU without touching foreground UI. A chat the
 *  host no longer has is retired rather than warmed again at the next boot. */
export async function warmSessionChatCache(
  client: LycaonClient,
  scope: SessionChatScope,
  _projects: readonly Project[],
  onSessionGone: (scope: SessionChatScope) => void,
): Promise<void> {
  if (getSessionChatCache(scope)) return;
  try {
    const { session, transcript } = await client.getSessionBootstrap(
      scope.sessionId,
    );
    putSessionChatCache({
      scope,
      touchedAt: Date.now(),
      session,
      transcript: {
        tail: transcript.messages.slice(),
        pages: {},
        hasTailGap: false,
        hasMoreBefore: Boolean(transcript.before_cursor),
        hasMoreAfter: Boolean(transcript.after_cursor),
      },
      messages: transcript.messages.slice(),
      transcriptWatermark: transcript.watermark,
      turnClocks: Object.values(transcript.turn_clocks),
      turnLoads: Object.values(transcript.turn_loads).flat(),
      workers: [],
      workerTranscripts: {},
      // Rows restore with the runs they name, or their spans have no run.
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
      pendingCheckpoints: [],
    });
  } catch (err) {
    if (isSessionNotFoundError(err)) onSessionGone(scope);
  }
}

export async function prefetchRecentSessionCaches(
  client: LycaonClient,
  recents: readonly RecentSession[],
  projects: readonly Project[],
  onSessionGone: (scope: SessionChatScope) => void,
): Promise<void> {
  const projectIds = prefetchProjectIds(recents);
  const scopes = firstRecentSessionByProject(recents, projectIds);
  for (const scope of scopes) {
    await warmSessionChatCache(client, scope, projects, onSessionGone);
  }
}
