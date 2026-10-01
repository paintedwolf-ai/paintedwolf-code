// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  installMainThreadPerfObserver,
  measureSync,
  perfMark,
  resetMainThreadPerfObserverForTests,
  uninstallMainThreadPerfObserver,
} from "./den-main-thread-perf.ts";
import { resetStreamScrollDebugForTests } from "./den-scroll-debug.ts";

describe("den-main-thread-perf", () => {
  const realTimeStamp = (console as unknown as { timeStamp?: unknown }).timeStamp;

  afterEach(() => {
    // Direct assignment requires explicit restoration.
    (console as unknown as { timeStamp?: unknown }).timeStamp = realTimeStamp;
    resetStreamScrollDebugForTests();
    resetMainThreadPerfObserverForTests();
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
    vi.useRealTimers();
    vi.restoreAllMocks();
    localStorage.removeItem("den:scroll-debug");
  });

  it("annotates an inspector timeline even with debug capture off", () => {
    localStorage.removeItem("den:scroll-debug");
    const stamp = vi.fn();
    (console as unknown as { timeStamp: unknown }).timeStamp = stamp;
    const mark = vi.spyOn(performance, "mark").mockImplementation(
      () => ({}) as PerformanceMark,
    );

    perfMark("prose:paint", { chars: 1200 });

    expect(stamp).toHaveBeenCalledWith("prose:paint chars=1200");
    expect(mark).not.toHaveBeenCalled();
  });

  it("falls back to User Timing when console timestamps are unavailable", () => {
    (console as unknown as { timeStamp?: unknown }).timeStamp = undefined;
    const mark = vi.spyOn(performance, "mark").mockImplementation(
      () => ({}) as PerformanceMark,
    );

    perfMark("prose:paint", { chars: 1200 });

    expect(mark).toHaveBeenCalledWith("prose:paint chars=1200");
  });

  it("a marker never throws into the path it annotates", () => {
    (console as unknown as { timeStamp: unknown }).timeStamp = () => {
      throw new Error("no inspector");
    };
    vi.spyOn(performance, "mark").mockImplementation(() => {
      throw new Error("buffer full");
    });
    // Diagnostic failures stay outside the measured operation.
    expect(() => perfMark("prose:paint", {})).not.toThrow();
  });

  it("runs the function and returns its value even when debug is off", () => {
    const spy = vi.fn(() => 42);
    expect(measureSync("x", spy)).toBe(42);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("logs a sync probe over threshold when debug is on", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    let clock = 0;
    vi.spyOn(performance, "now").mockImplementation(() => {
      const v = clock;
      clock += 25; // second call in measureSync is 25ms later
      return v;
    });

    const out = measureSync("transcript.rebuild", () => "ok", { msgs: 3 }, 8);

    expect(out).toBe("ok");
    expect(info).toHaveBeenCalledWith(
      expect.stringContaining("[den:perf] sync"),
    );
    expect(info.mock.calls.some((c) => String(c[0]).includes("transcript.rebuild"))).toBe(
      true,
    );
  });

  it("does not log a sync probe under threshold", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    vi.spyOn(performance, "now").mockReturnValue(0); // zero elapsed

    measureSync("fast", () => "ok", {}, 8);

    expect(info).not.toHaveBeenCalled();
  });

  it("perfMark is a no-op when debug is off", () => {
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    perfMark("prose:paint", { chars: 100 });
    expect(info).not.toHaveBeenCalled();
  });

  it("attributes a detected stall to the recent breadcrumb trail", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    vi.useFakeTimers();
    let clock = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => clock);

    // Sampler arms: expected wake = 1000 + 250.
    installMainThreadPerfObserver();
    // The marked operation delays the next sampler wake.
    perfMark("thumbnail.capture:start", {});
    clock = 2150; // actual - expected = 900ms >= stall threshold
    vi.advanceTimersByTime(250);

    const stall = info.mock.calls
      .map((c) => String(c[0]))
      .find((s) => s.includes("loop-stall"));
    expect(stall).toBeDefined();
    expect(stall).toContain("thumbnail.capture:start");

    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("names platform timer throttling apart from a main-thread stall", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    // Background timer throttling leaves the main thread idle.
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    vi.useFakeTimers();
    let clock = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => clock);

    installMainThreadPerfObserver();
    clock = 2000;
    vi.advanceTimersByTime(250);

    const lines = info.mock.calls.map((c) => String(c[0]));
    expect(lines.find((s) => s.includes("loop-throttled"))).toBeDefined();
    expect(lines.find((s) => s.includes("loop-stall"))).toBeUndefined();

    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("summarizes fast work without logging each sample", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    vi.useFakeTimers();
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    let clock = 0;
    vi.spyOn(performance, "now").mockImplementation(() => clock);
    installMainThreadPerfObserver();
    for (let i = 0; i < 100; i++) measureSync("resize.deliver", () => { clock += 0.5; });
    expect(info).not.toHaveBeenCalled();
    clock = 1000;
    vi.advanceTimersByTime(250);
    const summary = info.mock.calls.map((call) => String(call[0])).find((line) => line.includes("sync-summary"));
    expect(summary).toContain("label=resize.deliver count=100 total_ms=50 max_ms=0.5 window_ms=1000");
  });

  it("disconnects the observer and clears the lag sampler", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    vi.useFakeTimers();
    const disconnect = vi.fn();
    class StubPerformanceObserver {
      observe = vi.fn();
      disconnect = disconnect;
    }
    vi.stubGlobal("PerformanceObserver", StubPerformanceObserver);

    installMainThreadPerfObserver();
    expect(vi.getTimerCount()).toBe(1);
    uninstallMainThreadPerfObserver();

    expect(disconnect).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
    vi.useRealTimers();
  });
});
