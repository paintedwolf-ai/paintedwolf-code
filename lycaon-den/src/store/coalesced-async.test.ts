import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createBoundedDebouncedAsyncScheduler,
  createCoalescedAsyncScheduler,
  createThrottledAsyncScheduler,
} from "./coalesced-async.ts";

afterEach(() => vi.useRealTimers());

describe("createBoundedDebouncedAsyncScheduler", () => {
  it("waits for quiet and keeps only the latest burst invalidation", async () => {
    vi.useFakeTimers();
    const run = vi.fn().mockResolvedValue(undefined);
    const scheduler = createBoundedDebouncedAsyncScheduler(run, 30, 100);

    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(20);
    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(29);
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(1);

    scheduler.cancel();
    vi.useRealTimers();
  });

  it("runs at the maximum wait during a stream that never becomes quiet", async () => {
    vi.useFakeTimers();
    const run = vi.fn().mockResolvedValue(undefined);
    const scheduler = createBoundedDebouncedAsyncScheduler(run, 30, 100);

    scheduler.schedule();
    for (let elapsed = 20; elapsed < 100; elapsed += 20) {
      await vi.advanceTimersByTimeAsync(20);
      scheduler.schedule();
    }
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(20);
    expect(run).toHaveBeenCalledTimes(1);

    scheduler.cancel();
    vi.useRealTimers();
  });

  it("stretches the wait after a slow run so churn cannot saturate the backend", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      const run = vi.fn(
        () => new Promise<void>((resolve) => setTimeout(resolve, 300)),
      );
      const scheduler = createBoundedDebouncedAsyncScheduler(run, 30, 100);

      scheduler.schedule();
      await vi.advanceTimersByTimeAsync(30);
      expect(run).toHaveBeenCalledTimes(1);
      scheduler.schedule();
      await vi.advanceTimersByTimeAsync(300);
      // The cooldown is capped at four times the bounded wait.
      await vi.advanceTimersByTimeAsync(399);
      expect(run).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(run).toHaveBeenCalledTimes(2);

      scheduler.cancel();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("createCoalescedAsyncScheduler", () => {
  it("debounces burst schedule calls", async () => {
    vi.useFakeTimers();
    const run = vi.fn().mockResolvedValue(undefined);
    const scheduler = createCoalescedAsyncScheduler(run, 30);
    scheduler.schedule();
    scheduler.schedule();
    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(29);
    expect(run).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(1);
    scheduler.cancel();
  });
});

describe("createThrottledAsyncScheduler", () => {
  it("keeps only one trailing refresh during a sustained stream", async () => {
    vi.useFakeTimers();
    const run = vi.fn().mockResolvedValue(undefined);
    const scheduler = createThrottledAsyncScheduler(run, 30);
    scheduler.schedule();
    scheduler.schedule();
    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(10);
    expect(run).toHaveBeenCalledTimes(1);
    scheduler.schedule();
    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(60);
    expect(run).toHaveBeenCalledTimes(2);
    scheduler.cancel();
  });

  it("holds a cooldown after a fast run", async () => {
    vi.useFakeTimers();
    const run = vi.fn().mockResolvedValue(undefined);
    const scheduler = createThrottledAsyncScheduler(run, 30);

    scheduler.schedule();
    vi.runAllTicks();
    expect(run).toHaveBeenCalledTimes(1);

    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(29);
    expect(run).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(2);

    scheduler.cancel();
    vi.useRealTimers();
  });

  it("re-reads a getter interval each tick", async () => {
    vi.useFakeTimers();
    const run = vi.fn().mockResolvedValue(undefined);
    let intervalMs = 100;
    const scheduler = createThrottledAsyncScheduler(run, () => intervalMs);

    scheduler.schedule();
    vi.runAllTicks();
    expect(run).toHaveBeenCalledTimes(1);

    // The next cooldown uses the updated interval.
    intervalMs = 10;
    scheduler.schedule();
    await vi.advanceTimersByTimeAsync(10);
    expect(run).toHaveBeenCalledTimes(2);

    scheduler.cancel();
    vi.useRealTimers();
  });
});
