import type { MessageEvent } from "../../../api/types.ts";
import type { AppStore } from "../../../store/app-state-model.ts";
import { queueWorkerTranscriptPatch } from "../../worker/worker-transcript-coalesce.ts";

const MAX_BUFFERED_SESSIONS = 64;
const MAX_BUFFERED_ROWS_PER_SESSION = 512;

/** Latest row per message, ordered for bounded eviction. */
const pendingByChildSession = new Map<string, Map<string, MessageEvent>>();

/** Hold child SSE until its worker run is available. */
export function bufferChildMessageEvent(
  childSessionId: string,
  event: MessageEvent,
): void {
  const sid = childSessionId.trim();
  const messageId = event.message.id.trim();
  if (!sid || !messageId) return;
  const rows = pendingByChildSession.get(sid) ?? new Map<string, MessageEvent>();
  rows.delete(messageId);
  rows.set(messageId, event);
  while (rows.size > MAX_BUFFERED_ROWS_PER_SESSION) {
    const oldest = rows.keys().next().value;
    if (oldest === undefined) break;
    rows.delete(oldest);
  }
  pendingByChildSession.delete(sid);
  pendingByChildSession.set(sid, rows);
  while (pendingByChildSession.size > MAX_BUFFERED_SESSIONS) {
    const oldest = pendingByChildSession.keys().next().value;
    if (oldest === undefined) break;
    pendingByChildSession.delete(oldest);
  }
}

/** Apply buffered rows to their worker runs. */
export function replayBufferedChildMessageEvents(
  appStore: AppStore,
  childSessionId: string,
): void {
  const sid = childSessionId.trim();
  if (!sid) return;
  const pending = pendingByChildSession.get(sid);
  if (!pending?.size) return;
  pendingByChildSession.delete(sid);
  const unresolved = new Map<string, MessageEvent>();
  for (const event of orderedEvents(pending)) {
    const row = event.message;
    if (!row?.id?.trim()) continue;
    const workerId = row.worker_id?.trim();
    const worker = workerId
      ? appStore.state.workers.find((candidate) => candidate.id === workerId)
      : undefined;
    if (!worker) {
      unresolved.set(row.id, event);
      continue;
    }
    queueWorkerTranscriptPatch(appStore, worker.id, row);
  }
  if (unresolved.size > 0) pendingByChildSession.set(sid, unresolved);
}

/** Remove and return latest rows buffered while a session was not foreground. */
export function takeBufferedMessageEvents(sessionId: string): MessageEvent[] {
  const sid = sessionId.trim();
  if (!sid) return [];
  const pending = pendingByChildSession.get(sid);
  if (!pending?.size) return [];
  pendingByChildSession.delete(sid);
  return orderedEvents(pending);
}

function orderedEvents(rows: Map<string, MessageEvent>): MessageEvent[] {
  return [...rows.values()].sort(
    (a, b) => (a.message.seq ?? 0) - (b.message.seq ?? 0),
  );
}

export function clearChildMessageBuffer(): void {
  pendingByChildSession.clear();
}
