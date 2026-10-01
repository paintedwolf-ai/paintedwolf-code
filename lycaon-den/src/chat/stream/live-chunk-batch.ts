import type { LiveStreamChunk } from "./session-live-stream.ts";

/** Render the first chunk immediately, then at most once per frame; completion flushes. */
export function createLiveChunkBatch(
  apply: (chunk: LiveStreamChunk) => void,
  scheduler = {
    request: (callback: FrameRequestCallback) => requestAnimationFrame(callback),
    cancel: (frame: number) => cancelAnimationFrame(frame),
  },
): { push: (chunk: LiveStreamChunk) => void; flush: () => void; cancel: () => void } {
  let pending: LiveStreamChunk | undefined;
  let frame: number | undefined;
  let started = false;
  const cancel = () => {
    if (frame !== undefined) scheduler.cancel(frame);
    frame = undefined;
    pending = undefined;
  };
  const flush = () => {
    const chunk = pending;
    cancel();
    if (chunk) apply(chunk);
  };
  return {
    push: (chunk) => {
      if (!started) {
        started = true;
        apply(chunk);
        return;
      }
      const next = { ...pending, ...chunk, reset: pending?.reset };
      if (chunk.token !== undefined) {
        next.token = chunk.reset ? chunk.token : (pending?.token ?? "") + chunk.token;
        next.reset = chunk.reset || pending?.reset;
      }
      pending = next;
      if (chunk.done) flush();
      else if (frame === undefined) frame = scheduler.request(flush);
    },
    flush,
    cancel,
  };
}
