import { configure } from "@solidjs/testing-library";
import { afterEach, beforeEach, vi } from "vitest";

const FAKED = ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date", "performance",
  "requestAnimationFrame", "cancelAnimationFrame"] as const;

/**
 * Runs the tests in scope on a clock that moves only when a test moves it. The returned
 * `elapse` runs everything due within the span. Testing Library's `waitFor` and `findBy*`
 * advance this clock by their poll interval between checks, so their timeouts are spans of
 * this clock too.
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
  // Testing Library drives a fake clock through Jest's timer API.
  Object.assign(globalThis, { jest: { advanceTimersByTime: (ms: number) => vi.advanceTimersByTime(ms) } });
  configure({
    unstable_advanceTimersWrapper: async (advance: () => unknown) => {
      await advance();
      // Settles the promise work the fired timers started before the next check.
      await vi.advanceTimersByTimeAsync(0);
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
