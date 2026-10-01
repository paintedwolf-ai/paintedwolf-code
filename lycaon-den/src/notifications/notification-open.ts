/**
 * Session open sink for OS notification clicks — Shell registers the same
 * resume path a recents-row click uses.
 */

type SessionOpenSink = (sessionId: string) => void;

let sink: SessionOpenSink | null = null;
let pendingSessionId: string | null = null;

export function registerNotificationSessionOpenSink(
  next: SessionOpenSink,
): () => void {
  sink = next;
  if (pendingSessionId) {
    const id = pendingSessionId;
    pendingSessionId = null;
    next(id);
  }
  return () => {
    if (sink === next) sink = null;
  };
}

/** Focus is handled by the caller; this only selects the session when present. */
export function openSessionFromNotification(
  sessionId: string | undefined,
): void {
  const id = sessionId?.trim();
  if (!id) return;
  if (sink) sink(id);
  else pendingSessionId = id;
}
