// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  beginShellLayoutBusy,
  endShellLayoutBusy,
  flushShellLayoutSettleForTests,
  isShellLayoutBusy,
  isShellLayoutUnstable,
  onShellLayoutSettled,
  resetShellLayoutBusyForTests,
  runShellLayoutTransaction,
  sustainShellLayoutBusy,
} from "./shell-layout-busy.ts";

describe("shell-layout-busy", () => {
  afterEach(async () => {
    vi.useRealTimers();
    await flushShellLayoutSettleForTests();
    resetShellLayoutBusyForTests();
    vi.restoreAllMocks();
  });

  it("tracks nested busy spans", async () => {
    expect(isShellLayoutBusy()).toBe(false);
    beginShellLayoutBusy();
    expect(isShellLayoutBusy()).toBe(true);
    beginShellLayoutBusy();
    endShellLayoutBusy();
    expect(isShellLayoutBusy()).toBe(true);
    endShellLayoutBusy();
    expect(isShellLayoutBusy()).toBe(false);
  });

  it("holds asynchronous geometry writes in one span", async () => {
    let finish: (() => void) | undefined;
    const pendingGeometry = new Promise<void>((resolve) => {
      finish = resolve;
    });

    const transaction = runShellLayoutTransaction(async () => {
      expect(isShellLayoutBusy()).toBe(true);
      await pendingGeometry;
      expect(isShellLayoutBusy()).toBe(true);
    });

    expect(isShellLayoutBusy()).toBe(true);
    finish?.();
    await transaction;
    expect(isShellLayoutBusy()).toBe(false);
  });

  it("releases an asynchronous span when its change fails", async () => {
    await expect(
      runShellLayoutTransaction(async () => {
        throw new Error("geometry failed");
      }),
    ).rejects.toThrow("geometry failed");
    expect(isShellLayoutBusy()).toBe(false);
  });

  it("notifies settle listeners after two frames", async () => {
    const listener = vi.fn();
    const stop = onShellLayoutSettled(listener);

    beginShellLayoutBusy();
    endShellLayoutBusy();
    expect(listener).not.toHaveBeenCalled();
    await flushShellLayoutSettleForTests();
    expect(listener).toHaveBeenCalledTimes(1);

    stop();
    beginShellLayoutBusy();
    endShellLayoutBusy();
    await flushShellLayoutSettleForTests();
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("stays unstable through settle", async () => {
    beginShellLayoutBusy();
    expect(isShellLayoutUnstable()).toBe(true);
    endShellLayoutBusy();
    expect(isShellLayoutBusy()).toBe(false);
    expect(isShellLayoutUnstable()).toBe(true);

    await flushShellLayoutSettleForTests();
    expect(isShellLayoutUnstable()).toBe(false);
  });

  it("holds one span across a stream of continuous resize events", async () => {
    vi.useFakeTimers();
    const listener = vi.fn();
    const stop = onShellLayoutSettled(listener);

    // Resize quiet marks the end of the span.
    sustainShellLayoutBusy();
    sustainShellLayoutBusy();
    sustainShellLayoutBusy();
    expect(isShellLayoutBusy()).toBe(true);

    await vi.advanceTimersByTimeAsync(100);
    // The quiet window keeps measurements gated.
    expect(isShellLayoutBusy()).toBe(true);

    sustainShellLayoutBusy();
    await vi.advanceTimersByTimeAsync(100);
    expect(isShellLayoutBusy()).toBe(true);

    await vi.advanceTimersByTimeAsync(200);
    expect(isShellLayoutBusy()).toBe(false);
    await flushShellLayoutSettleForTests();
    expect(listener).toHaveBeenCalledTimes(1);

    stop();
    vi.useRealTimers();
  });

  it("nests a bounded drag inside a continuous resize without settling early", async () => {
    vi.useFakeTimers();
    const listener = vi.fn();
    const stop = onShellLayoutSettled(listener);

    sustainShellLayoutBusy();
    beginShellLayoutBusy();
    endShellLayoutBusy();
    // The sustained token still holds the span open.
    expect(isShellLayoutBusy()).toBe(true);
    expect(listener).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(200);
    expect(isShellLayoutBusy()).toBe(false);
    await flushShellLayoutSettleForTests();
    expect(listener).toHaveBeenCalledTimes(1);

    stop();
    vi.useRealTimers();
  });

  it("clears the busy class when the last span ends", async () => {
    beginShellLayoutBusy();
    expect(
      document.documentElement.classList.contains("den-shell--layout-busy"),
    ).toBe(true);
    endShellLayoutBusy();
    expect(isShellLayoutBusy()).toBe(false);
    expect(
      document.documentElement.classList.contains("den-shell--layout-busy"),
    ).toBe(false);

    await flushShellLayoutSettleForTests();
  });
});
