/** Store state transitions trigger native notifications. */

import type { SessionStatus } from "../api/types.ts";
import type { DenNotification, EnsureNotificationPermissionResult } from "../platform/desktop/notifications.ts";
import type { DenNotificationPrefs } from "../../shared/app-state-types.ts";
import { resolveNotificationPrefs } from "../settings/chat/notification-prefs.ts";

export const MEANINGFUL_RUN_MS = 4000;
export const NOTIFICATION_PRODUCT_TITLE = "Painted Wolf Code";
export const DEDUPE_CAP = 256;

export type NotificationClass = "Finished" | "NeedsYou" | "NeedsApproval";

const CLASS_BODY_LABEL: Record<NotificationClass, string> = {
  Finished: "Finished",
  NeedsYou: "Needs your input",
  NeedsApproval: "Needs your approval",
};

export interface NotificationServiceDeps {
  isFocused: () => boolean;
  prefs: () => DenNotificationPrefs;
  runActiveMs: (sessionId: string) => number | undefined;
  sessionTitle: (sessionId: string) => string | undefined;
  /** Finished transitionKey discriminator (ord / watermark). */
  runOrdinal: (sessionId: string) => string | number | undefined;
  send: (n: DenNotification) => Promise<void>;
  ensurePermission: () => Promise<EnsureNotificationPermissionResult>;
  onPermissionResult: (result: EnsureNotificationPermissionResult) => void;
}

/** Rising-edge inputs for one observe tick. */
export type NotificationObserveState = {
  sessionStatus: ReadonlyMap<string, SessionStatus>;
  /** sessionId → pending user-input request id while blocked. */
  pendingUserInput: ReadonlyMap<string, string>;
  /** checkpointId → sessionId for pending HITL approvals. */
  pendingCheckpoints: ReadonlyMap<string, string>;
};

class BoundedKeySet {
  private readonly order: string[] = [];
  private readonly keys = new Set<string>();

  constructor(private readonly cap: number) {}

  has(key: string): boolean {
    return this.keys.has(key);
  }

  add(key: string): void {
    if (this.keys.has(key)) return;
    this.keys.add(key);
    this.order.push(key);
    while (this.order.length > this.cap) {
      const oldest = this.order.shift();
      if (oldest !== undefined) this.keys.delete(oldest);
    }
  }
}

function classPrefEnabled(
  prefs: DenNotificationPrefs,
  cls: NotificationClass,
): boolean {
  const resolved = resolveNotificationPrefs(prefs);
  if (!resolved.enabled) return false;
  switch (cls) {
    case "Finished":
      return resolved.finished;
    case "NeedsYou":
      return resolved.needsYou;
    case "NeedsApproval":
      return resolved.needsApproval;
  }
}

function transitionKey(
  cls: NotificationClass,
  sessionId: string,
  discriminator: string,
): string {
  return `${cls}:${sessionId}:${discriminator}`;
}

function notificationBody(
  cls: NotificationClass,
  sessionTitle: string | undefined,
): string {
  const label = CLASS_BODY_LABEL[cls];
  const title = sessionTitle?.trim() || "session";
  return `${label} — ${title}`;
}

export type NotificationService = {
  /** Feed a store snapshot. First call seeds; later calls emit rising edges. */
  observe: (state: NotificationObserveState) => void;
  dispose: () => void;
};

export function createNotificationService(
  deps: NotificationServiceDeps,
): NotificationService {
  const seen = new BoundedKeySet(DEDUPE_CAP);
  let prevStatus = new Map<string, SessionStatus>();
  let prevUserInput = new Map<string, string>();
  let prevCheckpoints = new Map<string, string>();
  let primed = false;
  let disposed = false;

  async function tryEmit(
    cls: NotificationClass,
    sessionId: string,
    discriminator: string,
  ): Promise<void> {
    if (disposed) return;
    try {
      if (!classPrefEnabled(deps.prefs(), cls)) return;
      if (deps.isFocused()) return;

      const key = transitionKey(cls, sessionId, discriminator);
      if (seen.has(key)) return;
      seen.add(key);

      const permission = await deps.ensurePermission();
      if (disposed) return;
      deps.onPermissionResult(permission);
      if (permission !== "granted") return;

      await deps.send({
        title: NOTIFICATION_PRODUCT_TITLE,
        body: notificationBody(cls, deps.sessionTitle(sessionId)),
        sessionId,
      });
    } catch (err) {
      console.debug("[notification-service] emit failed", err);
    }
  }

  function observe(state: NotificationObserveState): void {
    if (disposed) return;
    try {
      if (!primed) {
        prevStatus = new Map(state.sessionStatus);
        prevUserInput = new Map(state.pendingUserInput);
        prevCheckpoints = new Map(state.pendingCheckpoints);
        primed = true;
        return;
      }

      for (const [sessionId, status] of state.sessionStatus) {
        const prior = prevStatus.get(sessionId);
        if (prior === "busy" && status === "idle") {
          const activeMs = deps.runActiveMs(sessionId);
          if (activeMs === undefined || activeMs < MEANINGFUL_RUN_MS) {
            continue;
          }
          const ordinal = deps.runOrdinal(sessionId);
          // Missing content identity leaves the transition silent.
          if (ordinal === undefined) continue;
          void tryEmit("Finished", sessionId, String(ordinal));
        }
      }

      for (const [sessionId, requestId] of state.pendingUserInput) {
        if (!prevUserInput.has(sessionId)) {
          void tryEmit("NeedsYou", sessionId, requestId);
        }
      }

      for (const [checkpointId, sessionId] of state.pendingCheckpoints) {
        if (!prevCheckpoints.has(checkpointId)) {
          void tryEmit("NeedsApproval", sessionId, checkpointId);
        }
      }

      prevStatus = new Map(state.sessionStatus);
      prevUserInput = new Map(state.pendingUserInput);
      prevCheckpoints = new Map(state.pendingCheckpoints);
    } catch (err) {
      console.debug("[notification-service] observe failed", err);
    }
  }

  return {
    observe,
    dispose: () => {
      disposed = true;
    },
  };
}
