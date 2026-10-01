// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { installMainThreadPerfObserver, measureSync, resetMainThreadPerfObserverForTests } from "./den-main-thread-perf.ts";
import { postDenPerfEvents } from "./den-perf-capture.ts";

vi.mock("./den-perf-capture.ts", () => ({
  postDenPerfEvents: vi.fn(),
}));
vi.mock("../../settings/system/debug-prefs.ts", () => ({
  fullDebugLoggingPref: () => true,
}));
vi.mock("./den-scroll-debug.ts", () => ({
  isStreamScrollDebugEnabled: () => false,
  denScrollDebugLog: vi.fn(),
}));
afterEach(() => {
  resetMainThreadPerfObserverForTests();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.mocked(postDenPerfEvents).mockClear();
});

describe("release performance capture", () => {
  it("batches operation counts and durations without logging each fast call", () => {
    vi.useFakeTimers();
    let now = 100;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    installMainThreadPerfObserver();
    expect(postDenPerfEvents).toHaveBeenCalledWith([{
      event: "clock-sync", detail: { perf_ms: 100, epoch_ms: expect.any(Number) },
    }]);
    vi.mocked(postDenPerfEvents).mockClear();
    for (let i = 0; i < 100; i++) measureSync("scrollbar.attach", () => { now += 0.5; });
    expect(postDenPerfEvents).not.toHaveBeenCalled();
    now = 1100;
    vi.advanceTimersByTime(250);
    expect(postDenPerfEvents).toHaveBeenCalledWith([{
      event: "sync-summary", channel: "perf",
      detail: { label: "scrollbar.attach", count: 100, total_ms: 50, max_ms: 0.5, window_ms: 1000 },
    }]);
  });

  it("includes measured duration in breadcrumbs instead of implying temporal proximity is attribution", () => {
    vi.useFakeTimers();
    let now = 0;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    installMainThreadPerfObserver();
    measureSync("transcript.rebuild", () => { now = 20; });
    now = 500;
    vi.advanceTimersByTime(250);
    const events = vi.mocked(postDenPerfEvents).mock.calls.flatMap(([batch]) => batch);
    expect(events.find((event) => event.event === "loop-stall")?.detail?.recent)
      .toBe("transcript.rebuild@-480(20ms)");
  });
});
