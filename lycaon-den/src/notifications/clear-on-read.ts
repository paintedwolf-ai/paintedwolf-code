import { cancelNotificationsForSessions } from "../platform/desktop/notifications.ts";

/**
 * Wraps a seen-stamp call so reading a chat also withdraws its desktop
 * notifications. The stamp is the source of truth; a failed stamp leaves the
 * notifications in place.
 */
export function clearNotificationsOnRead(
  mark: (sessionId: string) => Promise<void>,
  cancel: (sessionIds: readonly string[]) => Promise<void> = cancelNotificationsForSessions,
): (sessionId: string) => Promise<void> {
  return async (sessionId) => {
    await mark(sessionId);
    await cancel([sessionId]);
  };
}
