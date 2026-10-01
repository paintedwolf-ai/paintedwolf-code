import type { LycaonClient } from "../../api/client.ts";
import type { QueueDraft, QueueMutateRequest } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { LycaonApiError } from "../../api/http.ts";
import { batch } from "solid-js";
import { reconcilePendingOnQueueDraft } from "../send/pending-sends.ts";

function installQueueDraft(
  appStore: AppStore,
  sessionId: string,
  epoch: number,
  draft: QueueDraft,
): boolean {
  return batch(() => {
    if (!appStore.actions.setQueueDraft(sessionId, epoch, draft)) return false;
    reconcilePendingOnQueueDraft(appStore, sessionId.trim(), draft);
    return true;
  });
}

/** Ignore refreshes older than their triggering event. */
export async function refreshQueue(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  minRevision = 0,
): Promise<QueueDraft | undefined> {
  if (!sessionId.trim()) return undefined;
  const epoch = appStore.state.sessionViewEpoch;
  const draft = await client.getSessionQueue(sessionId);
  if (minRevision > 0 && draft.revision < minRevision) {
    return undefined;
  }
  return installQueueDraft(appStore, sessionId, epoch, draft) ? draft : undefined;
}

async function mutateQueue(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  body: Omit<QueueMutateRequest, "expected_revision">,
): Promise<void> {
  if (!sessionId.trim()) return;
  const epoch = appStore.state.sessionViewEpoch;
  const expectedRevision = appStore.state.queueDraft?.revision ?? 0;
  try {
    const draft = await client.updateSessionQueue(sessionId, {
      ...body,
      expected_revision: expectedRevision,
    });
    installQueueDraft(appStore, sessionId, epoch, draft);
  } catch (err) {
    if (
      err instanceof LycaonApiError && err.code === "queue_revision_conflict" &&
      appStore.state.sessionViewEpoch === epoch &&
      appStore.state.currentSession?.id.trim() === sessionId.trim()
    ) {
      await refreshQueue(appStore, client, sessionId);
    }
    throw err;
  }
}

export const removeQueueItems = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  itemIds: string[],
) => mutateQueue(appStore, client, sessionId, { op: "remove", item_ids: itemIds });

export const reorderQueue = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  orderedIds: string[],
) => mutateQueue(appStore, client, sessionId, { op: "reorder", item_ids: orderedIds });

export const updateQueueItem = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  itemId: string,
  text: string,
) => mutateQueue(appStore, client, sessionId, { op: "update", item_ids: [itemId], text });

export const linkQueueItems = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  itemIds: string[],
) => mutateQueue(appStore, client, sessionId, { op: "link", item_ids: itemIds });

export const unlinkQueueItems = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  itemIds: string[],
) => mutateQueue(appStore, client, sessionId, { op: "unlink", item_ids: itemIds });

export const fireQueueItemNow = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  itemId: string,
) => mutateQueue(appStore, client, sessionId, { op: "fire_now", item_ids: [itemId] });

export const sendQueue = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
) => mutateQueue(appStore, client, sessionId, { op: "send" });

/** Return an unclaimed reservation to the editable queue. */
export const cancelQueueSend = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
) => mutateQueue(appStore, client, sessionId, { op: "cancel_send" });

export const setQueueHold = (
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  hold: boolean,
) => mutateQueue(appStore, client, sessionId, { op: "hold", hold });
