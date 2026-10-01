import { createEffect, createRoot, untrack } from "solid-js";

/**
 * Marks a session read while its conversation is on screen in a focused
 * window, and again whenever its transcript advances there.
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
      void Promise.resolve(deps.mark(sessionId)).catch(() => undefined);
    });
    return dispose;
  });
}
