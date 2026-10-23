import { createEffect, createRoot, untrack } from "solid-js";

/**
 * Marks a session read while its conversation is on screen in a focused
 * window, and again whenever its transcript advances there.
 *
 * Each session has at most one stamp in flight. Changes during a post collapse
 * into one trailing post, sent only if the session is still readable: a busy
 * transcript must not queue a stamp per message and starve other requests.
 */
type SeenMarkerDeps = {
  /** Session whose conversation is readable right now; null when none is. */
  readable: () => string | null;
  /** Any value that changes when a turn may have completed; compared for equality. */
  revision: () => string;
  /** Posts the stamp. Failures are dropped: the next change re-marks. */
  mark: (sessionId: string) => Promise<unknown>;
  /** Runs as a session becomes readable, before this visit's stamp posts. */
  onReadable?: (sessionId: string) => void;
};

/** Starts the marker and returns its disposer. */
export function createSeenMarker(deps: SeenMarkerDeps): () => void {
  return createRoot((dispose) => {
    let marked = "";
    let readableBefore: string | null = null;
    let disposed = false;
    const inFlight = new Set<string>();
    const trailing = new Set<string>();

    const post = (sessionId: string) => {
      if (inFlight.has(sessionId)) {
        trailing.add(sessionId);
        return;
      }
      inFlight.add(sessionId);
      let pending: Promise<unknown>;
      try {
        pending = Promise.resolve(deps.mark(sessionId));
      } catch (err) {
        pending = Promise.reject(err);
      }
      void pending
        .catch(() => undefined)
        .finally(() => {
          inFlight.delete(sessionId);
          if (!trailing.delete(sessionId) || disposed) return;
          if (untrack(deps.readable) === sessionId) post(sessionId);
        });
    };

    createEffect(() => {
      const sessionId = deps.readable();
      const revision = deps.revision();
      const becameReadable = sessionId !== null && sessionId !== readableBefore;
      readableBefore = sessionId;
      if (!sessionId) return;
      if (becameReadable) untrack(() => deps.onReadable?.(sessionId));
      const key = `${sessionId}:${revision}`;
      if (key === marked) return;
      marked = key;
      post(sessionId);
    });
    return () => {
      disposed = true;
      trailing.clear();
      dispose();
    };
  });
}
