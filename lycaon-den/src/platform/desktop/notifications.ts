import { isTauriRuntime, tauriPlatform } from "../runtime.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

export interface DenNotification {
  title: string;
  body: string;
  /** Identifies the session when a notification is activated. */
  sessionId?: string;
}

export type EnsureNotificationPermissionResult =
  | "granted"
  | "denied"
  | "unavailable";

/**
 * `inhibited`: the service accepted the notification but is withholding
 * banners (do not disturb). `skipped`: nothing was handed to a service.
 */
export type NotificationDeliveryOutcome = "delivered" | "inhibited" | "skipped";

export const NOTIFICATION_ACTIVATED_EVENT = "notifications://activated";

type PluginModule = typeof import("@tauri-apps/plugin-notification");

type NativeAuthState =
  | "not_determined"
  | "denied"
  | "authorized"
  | "provisional"
  | "ephemeral"
  | "unavailable";

function usesNativeNotifications(): boolean {
  return isTauriRuntime() && (tauriPlatform() === "macos" || tauriPlatform() === "linux");
}

function nativeAuthAllowsDelivery(state: NativeAuthState): boolean {
  return (
    state === "authorized" ||
    state === "provisional" ||
    state === "ephemeral"
  );
}

async function loadPlugin(): Promise<PluginModule | null> {
  if (!isTauriRuntime() || usesNativeNotifications()) return null;
  try {
    return await import("@tauri-apps/plugin-notification");
  } catch (err) {
    console.debug("[notifications] plugin import failed", err);
    return null;
  }
}

async function nativePermissionState(): Promise<NativeAuthState> {
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<NativeAuthState>("den_notify_permission_state");
}

async function nativeRequestPermission(): Promise<NativeAuthState> {
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<NativeAuthState>("den_notify_request_permission");
}

export async function ensureNotificationPermission(): Promise<EnsureNotificationPermissionResult> {
  if (!isTauriRuntime()) return "unavailable";
  try {
    if (usesNativeNotifications()) {
      const live = await nativePermissionState();
      if (live === "unavailable") return "unavailable";
      if (nativeAuthAllowsDelivery(live)) return "granted";
      if (live === "denied") return "denied";
      const requested = await nativeRequestPermission();
      if (requested === "unavailable") return "unavailable";
      if (requested === "denied") return "denied";
      return nativeAuthAllowsDelivery(requested) ? "granted" : "unavailable";
    }
    const plugin = await loadPlugin();
    if (!plugin) return "unavailable";
    if (await plugin.isPermissionGranted()) return "granted";
    const result = await plugin.requestPermission();
    return result === "granted" || result === "denied" ? result : "unavailable";
  } catch (err) {
    console.debug("[notifications] permission request failed", err);
    return "unavailable";
  }
}

export async function cancelNotificationsForSessions(
  sessionIds: readonly string[],
): Promise<void> {
  const ids = [...new Set(sessionIds.map((id) => id.trim()).filter(Boolean))];
  if (ids.length === 0 || !usesNativeNotifications()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("den_notify_cancel_sessions", { sessionIds: ids });
  } catch (err) {
    console.debug("[notifications] cancel failed", err);
  }
}

export async function sendNotification(
  n: DenNotification,
): Promise<NotificationDeliveryOutcome> {
  if (!isTauriRuntime()) return "skipped";
  try {
    if (usesNativeNotifications()) {
      const live = await nativePermissionState();
      if (!nativeAuthAllowsDelivery(live)) return "skipped";
      const { invoke } = await import("@tauri-apps/api/core");
      const outcome = await invoke<unknown>("den_notify_send", {
        title: n.title,
        body: n.body,
        sessionId: n.sessionId ?? null,
      });
      return outcome === "inhibited" ? "inhibited" : "delivered";
    }
    const plugin = await loadPlugin();
    if (!plugin) return "skipped";
    if (!(await plugin.isPermissionGranted())) return "skipped";
    plugin.sendNotification({ title: n.title, body: n.body });
    return "delivered";
  } catch (err) {
    console.debug("[notifications] send failed", err);
    return "skipped";
  }
}

export function onNotificationActivated(
  cb: (sessionId: string | undefined) => void,
): () => void {
  if (!usesNativeNotifications()) return () => undefined;

  let unsubscribed = false;
  let unlistenEvent: (() => void) | null = null;

  void (async () => {
    try {
      unlistenEvent = await listenHostEvent<{ sessionId?: string | null }>(
        NOTIFICATION_ACTIVATED_EVENT,
        (event) => {
          if (unsubscribed) return;
          try {
            const sid = event.payload?.sessionId;
            cb(typeof sid === "string" && sid.length > 0 ? sid : undefined);
          } catch (err) {
            console.debug("[notifications] activation callback failed", err);
          }
        },
      );
      if (unsubscribed) {
        unlistenEvent();
        unlistenEvent = null;
      }
    } catch (err) {
      console.debug("[notifications] activation subscribe failed", err);
    }
  })();

  return () => {
    unsubscribed = true;
    const unlisten = unlistenEvent;
    unlistenEvent = null;
    if (unlisten) unlisten();
  };
}
