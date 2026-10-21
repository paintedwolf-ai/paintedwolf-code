import { createEffect, createRoot, onCleanup } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type { AttentionStore } from "../store/attention-store.ts";
import {
  cancelNotificationsForSessions, ensureNotificationPermission,
  onNotificationActivated, sendNotification,
} from "../platform/desktop/notifications.ts";
import { focusAppWindow, windowFocused } from "../platform/windows/window-chrome.ts";
import { setWaitingBadge } from "../platform/desktop/badge.ts";
import { isTauriRuntime } from "../platform/runtime.ts";
import { recordNotificationPermission, notificationPrefsLive } from "../settings/chat/notification-prefs.ts";
import { createNotificationService } from "./notification-service.ts";
import { finishedRun } from "./finished-run.ts";
import { openSessionFromNotification } from "./notification-open.ts";

export type NotificationMountStores = {
  attention: AttentionStore;
  getClient: () => LycaonClient | null;
};

export function mountNotificationService(stores: NotificationMountStores): () => void {
  if (!isTauriRuntime()) return () => undefined;
  return createRoot((dispose) => {
    const svc = createNotificationService({
      isFocused: () => windowFocused(),
      prefs: () => notificationPrefsLive(),
      connection: stores.getClient,
      finishedRun: async (row, signal) => {
        const client = stores.getClient();
        if (!client) return undefined;
        const snapshot = await client.getSessionBootstrap(row.session_id, { signal });
        if (signal.aborted || client !== stores.getClient()) return undefined;
        return finishedRun(snapshot, row);
      },
      send: sendNotification,
      cancel: cancelNotificationsForSessions,
      ensurePermission: ensureNotificationPermission,
      onPermissionResult: recordNotificationPermission,
    });
    createEffect(() => svc.observe(stores.attention.state.rows, stores.attention.state.loaded));
    createEffect(() => void setWaitingBadge(stores.attention.state.rows.filter(row => row.class === "needs_you").length));
    const unsubClick = onNotificationActivated((sessionId) => {
      void focusAppWindow();
      openSessionFromNotification(sessionId);
    });
    onCleanup(() => { unsubClick(); svc.dispose(); });
    return dispose;
  });
}
