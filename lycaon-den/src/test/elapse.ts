import { afterEach, beforeEach, vi } from "vitest";

/**
 * Lets the tests in scope pass time without waiting for it. The clock still
 * follows real time, so polling helpers keep working; the returned `elapse`
 * runs everything due within the span at once.
 */
export function useElapsableClock(): (ms: number) => Promise<void> {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => {
    vi.useRealTimers();
  });
  return async (ms) => {
    await vi.advanceTimersByTimeAsync(ms);
  };
}
