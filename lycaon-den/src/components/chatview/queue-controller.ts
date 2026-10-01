import { createSignal, onCleanup, type Accessor } from "solid-js";
import {
  cancelQueueSend,
  fireQueueItemNow,
  sendQueue,
  linkQueueItems,
  removeQueueItems,
  reorderQueue,
  setQueueHold,
  unlinkQueueItems,
  updateQueueItem,
} from "../../chat/actions/queue-actions.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { reportSessionNoticeError } from "../../notices/notice-store.ts";
import type { AppStore } from "../../store/app-state-model.ts";

export type QueueControllerDeps = {
  appStore: AppStore;
  sessionId: Accessor<string>;
  /** Marks an explicit user send. */
  onSend?: () => void;
};

export type QueueController = {
  arranging: Accessor<boolean>;
  /** Preserves an explicit pause across arrangement holds. */
  beginArrange: () => void;
  endArrange: () => void;
  /** Sets the persistent pause state. */
  setPaused: (paused: boolean) => void;
  fireNow: (itemId: string) => void;
  remove: (itemId: string) => void;
  link: (itemIds: string[]) => void;
  unlink: (itemIds: string[]) => void;
  reorder: (orderedIds: string[]) => void;
  update: (itemId: string, text: string) => void;
  send: () => void;
  /** Releases a reservation the turn has not taken yet. */
  cancelSend: () => void;
};

/** Controls persistent pauses and transient arrangement holds. */
export function createQueueController(deps: QueueControllerDeps): QueueController {
  const [arranging, setArranging] = createSignal(false);
  let arrangeDepth = 0;
  let holdBeforeArrange = false;

  const runQueueAction = (
    action: (
      appStore: AppStore,
      client: NonNullable<ReturnType<typeof getLycaonClient>>,
      sessionId: string,
    ) => Promise<void>,
  ) => {
    const client = getLycaonClient();
    if (!client) return;
    const sessionId = deps.sessionId();
    const projectId = deps.appStore.state.currentSession?.project_id ?? "";
    void action(deps.appStore, client, sessionId).catch((err: unknown) => {
      // Queue failures have no inline status surface.
      reportSessionNoticeError(err, projectId, sessionId);
    });
  };

  const beginArrange = () => {
    arrangeDepth += 1;
    if (arrangeDepth > 1) return;
    holdBeforeArrange = deps.appStore.state.queueDraft?.hold ?? false;
    setArranging(true);
    if (!holdBeforeArrange) {
      runQueueAction((s, c, sid) => setQueueHold(s, c, sid, true));
    }
  };

  const endArrange = () => {
    if (arrangeDepth === 0) return;
    arrangeDepth -= 1;
    if (arrangeDepth > 0) return;
    setArranging(false);
    if (!holdBeforeArrange) {
      runQueueAction((s, c, sid) => setQueueHold(s, c, sid, false));
    }
  };

  // Release transient holds when the card unmounts.
  onCleanup(() => {
    if (arrangeDepth > 0 && !holdBeforeArrange) {
      arrangeDepth = 0;
      runQueueAction((s, c, sid) => setQueueHold(s, c, sid, false));
    }
  });

  return {
    arranging,
    beginArrange,
    endArrange,
    setPaused: (paused) => {
      // Rebase the restored hold state during arrangement.
      holdBeforeArrange = paused;
      runQueueAction((s, c, sid) => setQueueHold(s, c, sid, paused));
    },
    fireNow: (id) => {
      deps.onSend?.();
      runQueueAction((s, c, sid) => fireQueueItemNow(s, c, sid, id));
    },
    remove: (id) => runQueueAction((s, c, sid) => removeQueueItems(s, c, sid, [id])),
    link: (ids) => runQueueAction((s, c, sid) => linkQueueItems(s, c, sid, ids)),
    unlink: (ids) => runQueueAction((s, c, sid) => unlinkQueueItems(s, c, sid, ids)),
    reorder: (ids) => runQueueAction((s, c, sid) => reorderQueue(s, c, sid, ids)),
    update: (id, text) =>
      runQueueAction((s, c, sid) => updateQueueItem(s, c, sid, id, text)),
    send: () => {
      deps.onSend?.();
      arrangeDepth = 0;
      holdBeforeArrange = false;
      setArranging(false);
      runQueueAction((s, c, sid) => sendQueue(s, c, sid));
    },
    cancelSend: () => runQueueAction((s, c, sid) => cancelQueueSend(s, c, sid)),
  };
}
