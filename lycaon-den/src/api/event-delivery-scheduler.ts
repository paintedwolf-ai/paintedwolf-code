/** Bound foreground batching while JavaScript runs; suspended processes cannot progress. */
export const EVENT_DELIVERY_MAX_WAIT_MS = 100;

export type EventDeliveryScheduler = {
  request: (callback: () => void) => number;
  cancel: (handle: number) => void;
};

type DeliveryJob = {
  frame?: number;
  deadline?: ReturnType<typeof setTimeout>;
  visibilityChanged?: () => void;
};

/** Host state must advance even when the webview stops producing paint frames. */
export function createEventDeliveryScheduler(): EventDeliveryScheduler {
  let nextHandle = 0;
  const jobs = new Map<number, DeliveryJob>();
  const visibility = typeof document === "undefined" ? undefined : document;

  const clearResources = (job: DeliveryJob) => {
    if (job.frame !== undefined) cancelAnimationFrame(job.frame);
    if (job.deadline !== undefined) clearTimeout(job.deadline);
    if (job.visibilityChanged) visibility?.removeEventListener("visibilitychange", job.visibilityChanged);
    job.frame = undefined;
    job.deadline = undefined;
    job.visibilityChanged = undefined;
  };

  const cancel = (handle: number) => {
    const job = jobs.get(handle);
    if (!job) return;
    jobs.delete(handle);
    clearResources(job);
  };

  return {
    request(callback) {
      const handle = ++nextHandle;
      const job: DeliveryJob = {};
      jobs.set(handle, job);
      const run = () => {
        if (jobs.get(handle) !== job) return;
        cancel(handle);
        callback();
      };
      if (visibility?.hidden || typeof requestAnimationFrame !== "function") {
        queueMicrotask(run);
      } else {
        job.visibilityChanged = () => {
          if (!visibility?.hidden) return;
          clearResources(job);
          queueMicrotask(run);
        };
        visibility?.addEventListener("visibilitychange", job.visibilityChanged);
        job.frame = requestAnimationFrame(run);
        job.deadline = setTimeout(run, EVENT_DELIVERY_MAX_WAIT_MS);
      }
      return handle;
    },
    cancel,
  };
}
