import { createEffect, createRoot, onCleanup } from "solid-js";
import type { SessionStatus } from "../api/types.ts";
import type { AppStore } from "../store/app-state-model.ts";
import { pendingWorkflowFeedback } from "../workflow/workflow-feedback-model.ts";
import { turnClockElapsedMs } from "../chat/session/turn-clock.ts";
import {
  ensureNotificationPermission,
  onNotificationActivated,
  sendNotification,
} from "../platform/desktop/notifications.ts";
import { focusAppWindow, windowFocused } from "../platform/windows/window-chrome.ts";
import { setWaitingBadge } from "../platform/desktop/badge.ts";
import { isTauriRuntime } from "../platform/runtime.ts";
import {
  recordNotificationPermission,
  notificationPrefsLive,
} from "../settings/chat/notification-prefs.ts";
import {
  createNotificationService,
  type NotificationObserveState,
} from "./notification-service.ts";
import { openSessionFromNotification } from "./notification-open.ts";

export type NotificationMountStores = {
  appStore: AppStore;
  /** Device-wide attention rows; the dock badge counts the ones waiting on a person. */
  waitingCount?: () => number;
};

/** Current-session status, pending asks, and pending checkpoints. */
function buildObserveState(appStore: AppStore): NotificationObserveState {
  const sessionStatus = new Map<string, SessionStatus>();
  const pendingUserInput = new Map<string, string>();
  const pendingCheckpoints = new Map<string, string>();

  const current = appStore.state.currentSession;
  if (current?.id) {
    sessionStatus.set(current.id, current.status);
  }

  for (const cp of appStore.state.pendingCheckpoints) {
    if (cp.status === "pending") {
      pendingCheckpoints.set(cp.checkpointId, cp.sessionId);
    }
  }

  const feedback = pendingWorkflowFeedback(appStore.state.activeWorkflowRun);
  if (feedback && current?.id) {
    pendingUserInput.set(current.id, feedback.phase_id);
  }

  return {
    sessionStatus,
    pendingUserInput,
    pendingCheckpoints,
  };
}

export function mountNotificationService(
  stores: NotificationMountStores,
): () => void {
  if (!isTauriRuntime()) return () => undefined;

  return createRoot((dispose) => {
    const svc = createNotificationService({
      isFocused: () => windowFocused(),
      prefs: () => notificationPrefsLive(),
      runActiveMs: (sessionId) => {
        const current = stores.appStore.state.currentSession?.id;
        if (current !== sessionId) return undefined;
        const clock = stores.appStore.state.turnClock;
        if (!clock) return undefined;
        return turnClockElapsedMs(clock, Date.now());
      },
      sessionTitle: (sessionId) => {
        const current = stores.appStore.state.currentSession;
        if (current?.id === sessionId) return current.title;
        return undefined;
      },
      runOrdinal: (sessionId) => {
        const current = stores.appStore.state.currentSession;
        if (current?.id !== sessionId) return undefined;
        const messages = stores.appStore.state.messages;
        for (let i = messages.length - 1; i >= 0; i--) {
          const ord = messages[i]?.ord;
          if (typeof ord === "number") return ord;
        }
        return undefined;
      },
      send: sendNotification,
      ensurePermission: ensureNotificationPermission,
      onPermissionResult: recordNotificationPermission,
    });

    createEffect(() => {
      stores.appStore.state.currentSession?.id;
      stores.appStore.state.currentSession?.status;
      stores.appStore.state.currentSession?.title;
      stores.appStore.state.pendingCheckpoints.length;
      for (const cp of stores.appStore.state.pendingCheckpoints) {
        cp.checkpointId;
        cp.status;
      }
      stores.appStore.state.activeWorkflowRun?.ui?.pending_feedback?.phase_id;
      stores.appStore.state.turnClock?.active_ms;
      stores.appStore.state.messages.length;
      windowFocused();
      notificationPrefsLive();

      svc.observe(buildObserveState(stores.appStore));
    });

    // Device-wide attention includes sessions outside this window.
    createEffect(() => {
      const count = stores.waitingCount
        ? stores.waitingCount()
        : stores.appStore.state.pendingCheckpoints.filter((cp) => cp.status === "pending").length;
      void setWaitingBadge(count);
    });

    const unsubClick = onNotificationActivated((sessionId) => {
      void focusAppWindow();
      openSessionFromNotification(sessionId);
    });

    onCleanup(() => {
      unsubClick();
      svc.dispose();
    });

    return dispose;
  });
}
