import { configure } from "@solidjs/testing-library";
import { afterEach, beforeEach, vi } from "vitest";

const FAKED = ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date", "performance",
  "requestAnimationFrame", "cancelAnimationFrame"] as const;
/** One animation frame. */
const POLL_STEP_MS = 16;

/**
 * Runs the tests in scope on a clock that moves only when a test moves it. The returned
 * `elapse` runs everything due within the span. Testing Library's `waitFor` and `findBy*`
 * advance this clock one frame between checks, so their timeouts are spans of this clock too.
 */
export function useElapsableClock(): (ms: number) => Promise<void> {
  beforeEach(startElapsableClock);
  afterEach(stopElapsableClock);
  return async (ms) => {
    await vi.advanceTimersByTimeAsync(ms);
  };
}

/** Starts the elapsable clock inside one test; `stopElapsableClock` must run after it. */
export function startElapsableClock(): void {
  vi.useFakeTimers({ toFake: [...FAKED] });
  let polling = false;
  // Testing Library drives a fake clock through Jest's timer API.
  Object.assign(globalThis, { jest: { advanceTimersByTime: (ms: number) => { if (!polling) vi.advanceTimersByTime(ms); } } });
  configure({
    unstable_advanceTimersWrapper: async (advance: () => unknown) => {
      // A poll's own 50 ms interval would fire several frames and timers back to back, with no
      // promise turn between them, and carry the clock past the moment its condition held.
      polling = true;
      try { await advance(); } finally { polling = false; }
      await vi.advanceTimersByTimeAsync(POLL_STEP_MS);
    },
  });
}

export function stopElapsableClock(): void {
  configure({ unstable_advanceTimersWrapper: (advance: () => unknown) => advance() });
  delete (globalThis as { jest?: unknown }).jest;
  vi.useRealTimers();
}

/** A host answer that takes `ms` on the elapsable clock. */
export function hostLatency(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
