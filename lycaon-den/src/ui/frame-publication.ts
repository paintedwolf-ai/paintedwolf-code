type AnimationFrameScheduler = {
  request: (callback: FrameRequestCallback) => number;
  cancel: (handle: number) => void;
};

const browserFrameScheduler: AnimationFrameScheduler = {
  request: (callback) => globalThis.requestAnimationFrame(callback),
  cancel: (handle) => globalThis.cancelAnimationFrame(handle),
};

/** Publish only the latest value scheduled during one rendered frame. */
export function createFramePublication<T>(
  publish: (value: T) => void,
  scheduler: AnimationFrameScheduler = browserFrameScheduler,
) {
  let frame: number | undefined;
  let pending: T | undefined;
  let hasPending = false;

  return {
    schedule(value: T): void {
      pending = value;
      hasPending = true;
      if (frame !== undefined) return;
      frame = scheduler.request(() => {
        frame = undefined;
        if (!hasPending) return;
        const next = pending as T;
        pending = undefined;
        hasPending = false;
        publish(next);
      });
    },
    cancel(): void {
      if (frame !== undefined) scheduler.cancel(frame);
      frame = undefined;
      pending = undefined;
      hasPending = false;
    },
  };
}
