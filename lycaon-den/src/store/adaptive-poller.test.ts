import { describe, expect, it, vi } from "vitest";
import { createAdaptivePoller } from "./adaptive-poller.ts";

describe("createAdaptivePoller", () => {
  it("backs off and stops", async () => {
    vi.useFakeTimers();
    const run = vi.fn(async () => undefined);
    const poller = createAdaptivePoller(run, 100, 400);
    poller.setEnabled(true);
    await vi.advanceTimersByTimeAsync(100);
    expect(run).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(199);
    expect(run).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(2);
    poller.setEnabled(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });
});
