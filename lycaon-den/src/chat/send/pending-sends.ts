import type { Message, QueueDraft } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { isChatActivityLive } from "../session/session-activity.ts";

export interface PendingSend {
  /** Composer destination, or a host-reserved queue head entering the transcript. */
  kind: "prompt" | "queued_prompt" | "queue_send";
  /** The id the host echoes as the user message id. */
  operationId: string;
  text: string;
  /** Local display timestamp, fixed when the pending row is added. */
  createdAt: number;
  attachmentLabels?: readonly string[];
  state: "sending" | "accepted";
}

/** The pending surface stays fixed until a host echo arrives. */
export function pendingPromptKind(appStore: AppStore, sessionId: string): "prompt" | "queued_prompt" {
  const queue = appStore.state.currentSession?.id === sessionId
    ? appStore.state.queueDraft : undefined;
  return isChatActivityLive(appStore, sessionId) || queue?.hold || queue?.queue_items.length ||
    appStore.state.pendingSends[sessionId]?.length
    ? "queued_prompt" : "prompt";
}

/** Host user rows carry the prompt operation id. */
export function reconcilePendingOnUserRow(
  appStore: AppStore,
  sessionId: string,
  row: Message,
): void {
  const pending = appStore.state.pendingSends[sessionId];
  if (!pending?.length) return;
  const id = row.id?.trim();
  if (!id || !pending.some((entry) => entry.operationId === id)) return;
  appStore.actions.removePendingSends(sessionId, [id]);
}

/** A linked head group lands as one message carrying the first item's id. */
function reservedHead(draft: QueueDraft): { id: string; text: string } | undefined {
  const items = draft.queue_items ?? (draft as { items?: typeof draft.queue_items }).items ?? [];
  const head = items[0];
  if (!draft.sending || !head) return undefined;
  const group = head.group_id
    ? items.filter((item) => item.group_id === head.group_id)
    : [head];
  const text = group
    .map((item) => item.text.trim())
    .filter(Boolean)
    .join("\n\n");
  return { id: head.id, text };
}

/**
 * A queue_send entry is the transcript seat of the draft's reserved head. A
 * queued entry of any other kind has handed off to the queue surface. A seat
 * whose item left the queue was consumed and waits for its echo.
 */
export function reconcilePendingOnQueueDraft(
  appStore: AppStore,
  sessionId: string,
  draft: QueueDraft,
): void {
  const pending = appStore.state.pendingSends[sessionId] ?? [];
  const items = draft.queue_items ?? (draft as { items?: typeof draft.queue_items }).items ?? [];
  const queued = new Set(items.map((item) => item.id));
  const head = reservedHead(draft);
  const released = pending
    .filter(
      (entry) =>
        queued.has(entry.operationId) &&
        (entry.kind !== "queue_send" || entry.operationId !== head?.id),
    )
    .map((entry) => entry.operationId);
  if (released.length > 0) {
    appStore.actions.removePendingSends(sessionId, released);
  }
  if (!head) return;
  if (
    pending.some(
      (entry) => entry.kind === "queue_send" && entry.operationId === head.id,
    )
  ) {
    return;
  }
  appStore.actions.addPendingSend(sessionId, {
    kind: "queue_send",
    operationId: head.id,
    text: head.text,
    state: "accepted",
  });
}

/** Resolve echoes delivered in a transcript baseline. */
export function reconcilePendingOnBaseline(
  appStore: AppStore,
  sessionId: string,
): void {
  const pending = appStore.state.pendingSends[sessionId];
  if (!pending?.length) return;
  if (appStore.state.transcriptSessionId?.trim() !== sessionId.trim()) return;
  const present = pending
    .filter((entry) =>
      appStore.state.messages.some((m) => m.id === entry.operationId),
    )
    .map((entry) => entry.operationId);
  if (present.length === 0) return;
  appStore.actions.removePendingSends(sessionId, present);
}

/** Remove accepted entries with no authoritative surface. */
export function sweepPendingOnIdle(appStore: AppStore, sessionId: string): void {
  const pending = appStore.state.pendingSends[sessionId];
  if (!pending?.length) return;
  // Only the foreground session has authoritative transcript and queue state.
  if (appStore.state.currentSession?.id?.trim() !== sessionId.trim()) return;
  const dead = pending
    .filter(
      (entry) =>
        entry.state === "accepted" &&
        !appStore.state.messages.some((m) => m.id === entry.operationId) &&
        !(appStore.state.queueDraft?.queue_items.some(
          (item) => item.id === entry.operationId,
        ) ?? false),
    )
    .map((entry) => entry.operationId);
  if (dead.length === 0) return;
  appStore.actions.removePendingSends(sessionId, dead);
}
