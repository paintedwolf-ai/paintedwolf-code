import { describe, expect, it, vi } from "vitest";
import {
  requestSourceRefresh,
  resetSourceRefreshForTests,
  setSourceRefresh,
  SOURCE_REFRESH_IDLE_MS,
  SOURCE_REFRESH_MAX_WAIT_MS,
} from "./source-refresh.ts";

describe("source refresh", () => {
  it("collapses a source_changed burst to one quiet-time refresh", async () => {
    vi.useFakeTimers();
    const refresh = vi.fn().mockResolvedValue(undefined);
    setSourceRefresh("project", refresh);

    requestSourceRefresh("project");
    requestSourceRefresh("project");
    requestSourceRefresh("project");
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS - 1);
    expect(refresh).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(1);

    resetSourceRefreshForTests();
    vi.useRealTimers();
  });

  it("retains coverage invalidation through coalescing and an active refresh", async () => {
    vi.useFakeTimers();
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const refresh = vi.fn().mockImplementationOnce(() => barrier).mockResolvedValue(undefined);
    setSourceRefresh("project", refresh);
    requestSourceRefresh("project");
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenNthCalledWith(1, { watchCoverage: false });
    requestSourceRefresh("project", { watchCoverage: true });
    requestSourceRefresh("project");
    release();
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenNthCalledWith(2, { watchCoverage: true });
    requestSourceRefresh("project");
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenNthCalledWith(3, { watchCoverage: false });
    resetSourceRefreshForTests();
    vi.useRealTimers();
  });

  it("refreshes within a bound while changes continue", async () => {
    vi.useFakeTimers();
    const refresh = vi.fn().mockResolvedValue(undefined);
    setSourceRefresh("project", refresh);

    requestSourceRefresh("project");
    for (
      let elapsed = SOURCE_REFRESH_IDLE_MS / 2;
      elapsed < SOURCE_REFRESH_MAX_WAIT_MS;
      elapsed += SOURCE_REFRESH_IDLE_MS / 2
    ) {
      await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS / 2);
      requestSourceRefresh("project");
    }
    expect(refresh).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS / 2);
    expect(refresh).toHaveBeenCalledTimes(1);

    resetSourceRefreshForTests();
    vi.useRealTimers();
  });
});
