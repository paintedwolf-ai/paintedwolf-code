// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { layoutViewportWidthPx, resetLayoutStoreForTests } from "./layout-store.ts";
import {
  flushShellLayoutSettleForTests,
  isShellLayoutBusy,
  resetShellLayoutBusyForTests,
} from "./shell-layout-busy.ts";
import { installWindowResizeEpoch } from "./window-resize.ts";

afterEach(async () => {
  vi.useRealTimers();
  await flushShellLayoutSettleForTests();
  resetShellLayoutBusyForTests();
  resetLayoutStoreForTests();
  vi.restoreAllMocks();
});

describe("window-resize epoch", () => {
  it("publishes viewport width once per frame and marks the epoch busy", async () => {
    vi.stubGlobal("innerWidth", 1200);
    const frames: FrameRequestCallback[] = [];
    vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
      frames.push(cb);
      return frames.length;
    });
    const stop = installWindowResizeEpoch();
    expect(layoutViewportWidthPx()).toBe(1200);
    expect(isShellLayoutBusy()).toBe(false);

    vi.stubGlobal("innerWidth", 800);
    window.dispatchEvent(new Event("resize"));
    window.dispatchEvent(new Event("resize"));
    expect(isShellLayoutBusy()).toBe(true);
    expect(layoutViewportWidthPx()).toBe(1200);
    expect(frames).toHaveLength(1);

    frames.shift()!(0);
    expect(layoutViewportWidthPx()).toBe(800);

    stop();
  });
});
