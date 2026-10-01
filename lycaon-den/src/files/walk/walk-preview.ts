import type { LycaonClient } from "../../api/client.ts";
import type { SourceWalkTurnSummary } from "../../api/types.ts";
import { createBoundedDebouncedAsyncScheduler } from "../../store/coalesced-async.ts";

type Listener = { messageId: string; notify: (summary: SourceWalkTurnSummary | null) => void };
type Preview = {
  client: LycaonClient;
  projectId: string;
  sessionId: string;
  listeners: Set<Listener>;
  scheduler: ReturnType<typeof createBoundedDebouncedAsyncScheduler>;
};
const previews = new Set<Preview>();

/** Cached turn summaries include empty results. */
type SessionAnswers = {
  projectId: string;
  sessionId: string;
  summaries: Map<string, SourceWalkTurnSummary | null>;
  /** Turns answered since the last source change. */
  fresh: Set<string>;
};

/** Maximum retained sessions after cards unmount. */
const ANSWERED_SESSION_MEMORY = 16;

// Cached answers preserve row height when virtualized cards remount.
const answers = new Map<string, SessionAnswers>();

function sessionKey(projectId: string, sessionId: string): string {
  return `${projectId}\0${sessionId}`;
}

function sessionAnswers(projectId: string, sessionId: string): SessionAnswers {
  const key = sessionKey(projectId, sessionId);
  const record = answers.get(key) ?? {
    projectId, sessionId, summaries: new Map(), fresh: new Set<string>(),
  };
  answers.delete(key);
  answers.set(key, record);
  if (answers.size > ANSWERED_SESSION_MEMORY) {
    const oldest = answers.keys().next().value;
    if (oldest !== undefined) answers.delete(oldest);
  }
  return record;
}

/** The host's last answer for a turn; undefined until it has answered. */
export function cachedWalkPreview(
  projectId: string, sessionId: string, messageId: string,
): SourceWalkTurnSummary | null | undefined {
  return answers.get(sessionKey(projectId, sessionId))?.summaries.get(messageId);
}

/** Cards share bounded requests for their visible turn summaries. */
export function subscribeWalkPreview(
  client: LycaonClient, projectId: string, sessionId: string, messageId: string,
  notify: Listener["notify"],
): () => void {
  let preview = [...previews].find((entry) => entry.client === client && entry.projectId === projectId && entry.sessionId === sessionId);
  if (!preview) {
    const entry: Preview = {
      client, projectId, sessionId, listeners: new Set(),
      scheduler: createBoundedDebouncedAsyncScheduler(async () => {
        const ids = [...new Set([...entry.listeners].map((listener) => listener.messageId))];
        try {
          const fetched = new Map<string, SourceWalkTurnSummary>();
          for (let start = 0; start < ids.length; start += 100) {
            const rows = await client.getProjectSourceWalkSummary(projectId, sessionId, ids.slice(start, start + 100));
            for (const row of rows) fetched.set(row.message_id, row);
            if (entry.listeners.size === 0) return;
          }
          const record = sessionAnswers(projectId, sessionId);
          for (const id of ids) {
            record.summaries.set(id, fetched.get(id) ?? null);
            record.fresh.add(id);
          }
          for (const listener of entry.listeners) listener.notify(record.summaries.get(listener.messageId) ?? null);
        } catch {
          // Failed refreshes preserve the cached preview.
        }
      }, 400, 2000),
    };
    preview = entry;
    previews.add(entry);
  }
  const held = preview;
  const listener = { messageId, notify };
  held.listeners.add(listener);
  const record = answers.get(sessionKey(projectId, sessionId));
  notify(record?.summaries.get(messageId) ?? null);
  // Source changes invalidate cached answers.
  if (!record?.fresh.has(messageId)) held.scheduler.schedule();
  return () => {
    held.listeners.delete(listener);
    if (held.listeners.size === 0) {
      held.scheduler.cancel();
      previews.delete(held);
    }
  };
}

export function refreshWalkPreviews(projectId: string, sessionId?: string): void {
  const pid = projectId.trim();
  const sid = sessionId?.trim();
  for (const record of answers.values()) {
    if (record.projectId === pid && (sid === undefined || record.sessionId === sid)) record.fresh.clear();
  }
  for (const preview of previews) {
    if (preview.projectId === pid && (sid === undefined || preview.sessionId === sid)) preview.scheduler.schedule();
  }
}

export function resetWalkPreviewsForTests(): void {
  for (const preview of previews) preview.scheduler.cancel();
  previews.clear();
  answers.clear();
}
