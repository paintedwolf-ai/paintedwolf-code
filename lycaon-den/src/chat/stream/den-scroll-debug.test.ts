// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  denScrollDebugLog,
  DEN_SCROLL_DEBUG_SINK_FLUSH_MS,
  isStreamScrollDebugEnabled,
  isStreamScrollTraceVerbose,
  resetStreamScrollDebugForTests,
} from "./den-scroll-debug.ts";

describe("den-scroll-debug", () => {
  afterEach(() => {
    resetStreamScrollDebugForTests();
    vi.useRealTimers();
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
    localStorage.removeItem("den:scroll-debug");
    vi.restoreAllMocks();
  });

  it("enables when VITE_DEN_SCROLL_DEBUG=1", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    expect(isStreamScrollDebugEnabled()).toBe(true);
  });

  it("localStorage 0 forces off even when vite flag is set", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    localStorage.setItem("den:scroll-debug", "0");
    expect(isStreamScrollDebugEnabled()).toBe(false);
  });

  it("the den:dev default level does not enable per-write traces", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    expect(isStreamScrollDebugEnabled()).toBe(true);
    expect(isStreamScrollTraceVerbose()).toBe(false);
  });

  it("level 2 enables per-write traces, from env or localStorage", () => {
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "2");
    expect(isStreamScrollTraceVerbose()).toBe(true);

    resetStreamScrollDebugForTests();
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "");
    localStorage.setItem("den:scroll-debug", "2");
    expect(isStreamScrollDebugEnabled()).toBe(true);
    expect(isStreamScrollTraceVerbose()).toBe(true);
  });

  it("batches JSONL over a fixed interval without console or beacon overhead", async () => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    const send = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", send);
    const beacon = vi.fn();
    vi.stubGlobal("navigator", { sendBeacon: beacon });
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    denScrollDebugLog("reveal", "settle-mount", { mounted: 2 });
    vi.advanceTimersByTime(DEN_SCROLL_DEBUG_SINK_FLUSH_MS - 1);
    denScrollDebugLog("scroll", "follow-settle", { frames: 4 });
    expect(send).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(send).toHaveBeenCalledTimes(1);
    const [url, request] = send.mock.calls[0]!;
    expect(url).toBe("/__den/scroll-debug");
    expect(request.body.trim().split("\n").map((line: string) => JSON.parse(line).event))
      .toEqual(["settle-mount", "follow-settle"]);
    expect(info).not.toHaveBeenCalled();
    expect(beacon).not.toHaveBeenCalled();
  });

  it("bounds a queue behind a slow request and reports discarded records", async () => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    let complete!: (value: { ok: boolean }) => void;
    const send = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }))
      .mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", send);
    denScrollDebugLog("scroll", "first");
    await vi.advanceTimersByTimeAsync(DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
    for (let i = 0; i < 1000; i++) denScrollDebugLog("scroll", "busy", { text: "x".repeat(1000) });
    await vi.advanceTimersByTimeAsync(DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
    expect(send).toHaveBeenCalledTimes(1);
    complete({ ok: true });
    await vi.advanceTimersByTimeAsync(DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
    expect(send).toHaveBeenCalledTimes(2);
    const body = send.mock.calls[1]![1].body as string;
    expect(body.length).toBeLessThan(25 * 1024);
    expect(JSON.parse(body.split("\n")[0]!).event).toBe("debug-sink-overflow");
  });

  it("reports an oversized record even when no other records are queued", async () => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    const send = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", send);
    denScrollDebugLog("perf", "oversized", { text: "x".repeat(30_000) });
    await vi.advanceTimersByTimeAsync(DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
    expect(send).toHaveBeenCalledTimes(1);
    expect(JSON.parse(send.mock.calls[0]![1].body)).toMatchObject({
      event: "debug-sink-overflow", dropped: 1,
    });
  });

  it("retains discarded-record counts when their report fails to reach the sink", async () => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    const send = vi.fn().mockResolvedValueOnce({ ok: false }).mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", send);
    denScrollDebugLog("perf", "oversized", { text: "x".repeat(30_000) });
    await vi.advanceTimersByTimeAsync(DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
    denScrollDebugLog("perf", "recovered");
    await vi.advanceTimersByTimeAsync(DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
    const firstLine = send.mock.calls[1]![1].body.split("\n")[0];
    expect(JSON.parse(firstLine)).toMatchObject({ event: "debug-sink-overflow", dropped: 1 });
  });

  it("flushes pending records on pagehide and releases all callbacks on disposal", () => {
    vi.useFakeTimers();
    vi.stubEnv("VITE_DEN_SCROLL_DEBUG", "1");
    const send = vi.fn();
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("fetch", send);
    vi.stubGlobal("navigator", { sendBeacon: beacon });
    denScrollDebugLog("perf", "last");
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledTimes(1);
    expect(beacon.mock.calls[0]![1].size).toBeGreaterThan(0);
    denScrollDebugLog("perf", "discard");
    resetStreamScrollDebugForTests();
    window.dispatchEvent(new Event("pagehide"));
    vi.runAllTimers();
    expect(beacon).toHaveBeenCalledTimes(1);
    expect(send).not.toHaveBeenCalled();
  });
});
