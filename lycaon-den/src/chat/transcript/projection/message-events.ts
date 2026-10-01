import type { Message, MessageEvent } from "../../../api/types.ts";
import type { AppStore } from "../../../store/app-state-model.ts";
import { reconcilePendingOnUserRow } from "../../send/pending-sends.ts";
import { llmTurnFor } from "../../session/session-activity.ts";
import { syncWorkerFromTaskToolMessage } from "../../worker/workers-model.ts";
import { queueWorkerTranscriptPatch } from "../../worker/worker-transcript-coalesce.ts";
import {
  bufferChildMessageEvent,
  takeBufferedMessageEvents,
} from "./worker-child-message-buffer.ts";

/** Apply transcript SSE or buffer it until the target session is ready. */
export function applyMessageEvent(appStore: AppStore, event: MessageEvent): boolean {
  const sessionId = event.session_id?.trim();
  const row = event.message;
  if (!sessionId || !row?.id?.trim()) return false;

  if (hydrationBlocksParentEvent(appStore, sessionId)) {
    appStore.actions.bufferHydrationMessageEvent(event);
    return false;
  }

  const currentId = appStore.state.currentSession?.id?.trim();
  if (currentId === sessionId) {
    const applied = appStore.actions.upsertMessage(row);
    // Stale echoes still resolve their pending send.
    reconcilePendingOnUserRow(appStore, sessionId, row);
    if (applied) {
      clearStaleLlmTurnActivity(appStore, sessionId, row);
      syncWorkerFromTaskToolMessage(appStore, sessionId, row);
    }
    return applied;
  }

  const workerId = row.worker_id?.trim();
  const worker = workerId
    ? appStore.state.workers.find((candidate) => candidate.id === workerId)
    : undefined;
  if (!worker) {
    bufferChildMessageEvent(sessionId, event);
    return false;
  }

  return queueWorkerTranscriptPatch(appStore, worker.id, row);
}

/** Finish hydration and replay buffered rows. */
export function completeChatHydrationAndReplay(appStore: AppStore): void {
  appStore.actions.completeChatSessionHydration();
  for (const event of appStore.actions.takeHydrationBuffer()) {
    applyMessageEvent(appStore, event);
  }
}

/** Replay buffered rows when a session enters the foreground. */
export function replayForegroundBufferedMessages(
  appStore: AppStore,
  sessionId: string,
): void {
  const buffered = takeBufferedMessageEvents(sessionId);
  for (const event of buffered) {
    applyMessageEvent(appStore, event);
  }
}

function hydrationBlocksParentEvent(
  appStore: AppStore,
  sessionId: string,
): boolean {
  const lock = appStore.state.chatHydrationLock?.trim();
  if (!lock) return false;
  return lock === "*" || lock === sessionId;
}

/** Clear coordinator turn chrome once authoritative SSE supersedes replay state. */
function clearStaleLlmTurnActivity(
  appStore: AppStore,
  sessionId: string,
  row: Message,
): void {
  const turn = llmTurnFor(appStore, sessionId);
  if (!turn) return;

  if (row.tool_calls?.length) {
    appStore.actions.clearLlmTurn(sessionId);
    return;
  }

  if (turn.status === "active") {
    return;
  }

  if (row.role === "assistant" && row.content.trim()) {
    appStore.actions.clearLlmTurn(sessionId);
  }
}
