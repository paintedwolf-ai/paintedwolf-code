// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let closeHandler: ((event: { preventDefault(): void }) => void | Promise<void>) | undefined;
const destroy = vi.fn(async () => undefined);
const disposeClose = vi.fn();
const onCloseRequested = vi.fn(async (handler: typeof closeHandler) => { closeHandler = handler; return disposeClose; });
const { invoke, handlers, nativeExit } = vi.hoisted(() => ({ nativeExit: { id: null as number | null }, invoke: vi.fn(), handlers: new Map<string, (event: { payload: { request_id: number } }) => void>() }));
vi.mock("@tauri-apps/api/window", () => ({ getCurrentWindow: () => ({ onCloseRequested, destroy }) }));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("./window-channel.ts", () => ({ listenHostEvent: async (name: string, handler: (event: { payload: { request_id: number } }) => void) => {
  handlers.set(name, handler);
  return () => { handlers.delete(name); };
} }));

import { watchWindowExit } from "./watch-window-exit.ts";
import { shortcutCommandsSuspended } from "../../shortcuts/dispatcher.ts";
const disposers: Array<() => void> = [];
function watch(...args: Parameters<typeof watchWindowExit>): () => void {
  const dispose = watchWindowExit(...args);
  disposers.push(dispose);
  return dispose;
}
async function installed(): Promise<void> { await vi.waitFor(() => expect(handlers.size).toBe(2)); }
const request = (id: number) => { nativeExit.id = id; handlers.get("app-exit-requested")?.({ payload: { request_id: id } }); };
const cancel = (id: number) => { if (nativeExit.id === id) nativeExit.id = null; handlers.get("app-exit-cancelled")?.({ payload: { request_id: id } }); };
const acknowledged = (id: number) => expect(invoke).toHaveBeenCalledWith("acknowledge_app_exit", { requestId: id });

beforeEach(() => {
  vi.useFakeTimers();
  closeHandler = undefined;
  destroy.mockReset().mockResolvedValue(undefined);
  disposeClose.mockReset();
  onCloseRequested.mockClear();
  nativeExit.id = null;
  invoke.mockReset().mockImplementation(async (name: string) => name === "pending_app_exit" && nativeExit.id !== null ? { request_id: nativeExit.id } : null);
  handlers.clear();
  document.documentElement.inert = false;
  vi.spyOn(console, "error").mockImplementation(() => undefined);
});
afterEach(async () => {
  for (const dispose of disposers.splice(0)) dispose();
  await vi.advanceTimersByTimeAsync(0);
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("durable window exit", () => {
  it("flushes on hide, pagehide, and beforeunload", async () => {
    const flush = vi.fn();
    const dispose = watch(flush);
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);
    window.dispatchEvent(new Event("beforeunload"));
    await vi.advanceTimersByTimeAsync(0);
    expect(flush).toHaveBeenCalledTimes(3);
    dispose();
    window.dispatchEvent(new Event("pagehide"));
    expect(flush).toHaveBeenCalledTimes(3);
  });

  it("coalesces overlapping flush signals", async () => {
    let finish!: () => void;
    const flush = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockResolvedValue(undefined);
    watch(flush);
    window.dispatchEvent(new Event("pagehide"));
    window.dispatchEvent(new Event("beforeunload"));
    expect(flush).toHaveBeenCalledOnce();
    finish();
    await vi.advanceTimersByTimeAsync(0);
    window.dispatchEvent(new Event("pagehide"));
    expect(flush).toHaveBeenCalledTimes(2);
  });

  it("ignores returning to the foreground", () => {
    const flush = vi.fn();
    watch(flush);
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(flush).not.toHaveBeenCalled();
  });

  it("preserves before releasing resources and destroying the window", async () => {
    let finishFlush!: () => void;
    let finishRelease!: () => void;
    const flush = vi.fn(() => new Promise<void>((resolve) => { finishFlush = resolve; }));
    const release = vi.fn(() => new Promise<void>((resolve) => { finishRelease = resolve; }));
    const resume = vi.fn();
    watch(flush, { beforeDestroy: release, onKeepOpen: resume });
    await installed();
    const done = closeHandler?.({ preventDefault: vi.fn() });
    expect(document.documentElement.inert).toBe(true);
    expect(release).not.toHaveBeenCalled();
    finishFlush();
    await vi.waitFor(() => expect(release).toHaveBeenCalledOnce());
    expect(destroy).not.toHaveBeenCalled();
    finishRelease();
    await done;
    expect(destroy).toHaveBeenCalledOnce();
    expect(resume).not.toHaveBeenCalled();
    expect(document.documentElement.inert).toBe(false);
  });

  it("automatically retries preservation before honoring the original close", async () => {
    const flush = vi.fn().mockRejectedValueOnce(new Error("temporarily unavailable")).mockResolvedValue(undefined);
    watch(flush);
    await installed();
    const done = closeHandler?.({ preventDefault: vi.fn() });
    await vi.advanceTimersByTimeAsync(1499);
    expect(flush).toHaveBeenCalledOnce();
    expect(destroy).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await done;
    expect(flush).toHaveBeenCalledTimes(2);
    expect(destroy).toHaveBeenCalledOnce();
  });

  it("takes a final snapshot after a hide flush that began before close", async () => {
    let finish!: () => void;
    const flush = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockResolvedValue(undefined);
    watch(flush);
    await installed();
    window.dispatchEvent(new Event("pagehide"));
    const done = closeHandler?.({ preventDefault: vi.fn() });
    expect(flush).toHaveBeenCalledOnce();
    expect(destroy).not.toHaveBeenCalled();
    finish();
    await done;
    expect(flush).toHaveBeenCalledTimes(2);
    expect(destroy).toHaveBeenCalledOnce();
  });

  it.each(["release", "destroy"])("automatically retries a %s failure and preserves again", async (failure) => {
    const flush = vi.fn();
    const release = vi.fn().mockResolvedValue(undefined);
    if (failure === "release") release.mockRejectedValueOnce(new Error("release failed"));
    else destroy.mockRejectedValueOnce(new Error("destroy failed"));
    const resume = vi.fn();
    watch(flush, { beforeDestroy: release, onKeepOpen: resume });
    await installed();
    const done = closeHandler?.({ preventDefault: vi.fn() });
    await vi.advanceTimersByTimeAsync(1500);
    await done;
    expect(flush).toHaveBeenCalledTimes(2);
    expect(resume).toHaveBeenCalledOnce();
    expect(destroy).toHaveBeenCalledTimes(failure === "destroy" ? 2 : 1);
  });

  it("keeps work and resources alive while preservation remains unavailable", async () => {
    const flush = vi.fn().mockRejectedValue(new Error("disk full"));
    const release = vi.fn();
    const dispose = watch(flush, { beforeDestroy: release, onKeepOpen: vi.fn() });
    await installed();
    const done = closeHandler?.({ preventDefault: vi.fn() });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(flush.mock.calls.length).toBeGreaterThan(1);
    expect(release).not.toHaveBeenCalled();
    expect(destroy).not.toHaveBeenCalled();
    dispose();
    await done;
  });

  it("coalesces repeated desktop close requests", async () => {
    let finish!: () => void;
    const flush = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    watch(flush);
    await installed();
    const done = closeHandler?.({ preventDefault: vi.fn() });
    const preventDefault = vi.fn();
    await closeHandler?.({ preventDefault });
    expect(preventDefault).toHaveBeenCalledOnce();
    expect(flush).toHaveBeenCalledOnce();
    finish();
    await done;
    expect(destroy).toHaveBeenCalledOnce();
  });
});

describe("application exit and update preservation", () => {
  it("automatically recovers listener installation before acknowledging a pending exit", async () => {
    onCloseRequested.mockRejectedValueOnce(new Error("temporary IPC failure"));
    nativeExit.id = 6;
    watch(vi.fn());
    await vi.advanceTimersByTimeAsync(0);
    expect(invoke).not.toHaveBeenCalledWith("acknowledge_app_exit", expect.anything());
    await vi.advanceTimersByTimeAsync(1500);
    await vi.waitFor(() => acknowledged(6));
    expect(onCloseRequested).toHaveBeenCalledTimes(2);
  });

  it("acknowledges only after preservation and resource release, keeping input frozen", async () => {
    let finish!: () => void;
    const flush = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const release = vi.fn();
    watch(flush, { beforeDestroy: release, onKeepOpen: vi.fn() });
    await installed();
    request(7);
    request(7);
    expect(flush).toHaveBeenCalledOnce();
    expect(document.documentElement.inert).toBe(true);
    expect(shortcutCommandsSuspended()).toBe(true);
    expect(invoke).not.toHaveBeenCalledWith("acknowledge_app_exit", expect.anything());
    finish();
    await vi.waitFor(() => acknowledged(7));
    expect(release).toHaveBeenCalledOnce();
    expect(destroy).not.toHaveBeenCalled();
    expect(document.documentElement.inert).toBe(true);
    expect(shortcutCommandsSuspended()).toBe(true);
  });

  it("recovers a request emitted before listener startup", async () => {
    invoke.mockImplementation(async (name: string) => name === "pending_app_exit" ? { request_id: 11 } : undefined);
    const flush = vi.fn();
    watch(flush);
    await vi.waitFor(() => acknowledged(11));
    expect(flush).toHaveBeenCalledOnce();
  });

  it("retries a failed acknowledgement without requiring another exit request", async () => {
    let attempts = 0;
    invoke.mockImplementation(async (name: string) => {
      if (name === "acknowledge_app_exit" && attempts++ === 0) throw new Error("temporary IPC failure");
      return name === "pending_app_exit" && nativeExit.id !== null ? { request_id: nativeExit.id } : null;
    });
    const flush = vi.fn();
    watch(flush);
    await installed();
    request(12);
    await vi.advanceTimersByTimeAsync(1500);
    await vi.waitFor(() => expect(attempts).toBe(2));
    expect(flush).toHaveBeenCalledTimes(2);
    expect(document.documentElement.inert).toBe(true);
  });

  it("resumes input and resources when installation fails, and preserves for the next request", async () => {
    const flush = vi.fn();
    const resume = vi.fn();
    watch(flush, { beforeDestroy: vi.fn(), onKeepOpen: resume });
    await installed();
    request(13);
    await vi.waitFor(() => acknowledged(13));
    cancel(12);
    expect(document.documentElement.inert).toBe(true);
    cancel(13);
    expect(document.documentElement.inert).toBe(false);
    expect(shortcutCommandsSuspended()).toBe(false);
    expect(resume).toHaveBeenCalledOnce();
    request(14);
    await vi.waitFor(() => acknowledged(14));
    expect(flush).toHaveBeenCalledTimes(2);
  });

  it("recovers a lost installation cancellation event", async () => {
    const resume = vi.fn();
    watch(vi.fn(), { beforeDestroy: vi.fn(), onKeepOpen: resume });
    await installed();
    request(16);
    await vi.waitFor(() => acknowledged(16));
    nativeExit.id = null;
    await vi.advanceTimersByTimeAsync(1500);
    expect(document.documentElement.inert).toBe(false);
    expect(resume).toHaveBeenCalledOnce();
  });

  it("cancels automatic preservation retries when the installation is abandoned", async () => {
    const flush = vi.fn().mockRejectedValue(new Error("disk unavailable"));
    watch(flush);
    await installed();
    request(15);
    await vi.advanceTimersByTimeAsync(0);
    cancel(15);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(flush).toHaveBeenCalledOnce();
    expect(document.documentElement.inert).toBe(false);
    expect(invoke).not.toHaveBeenCalledWith("acknowledge_app_exit", expect.anything());
  });
});
