// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  isScrollMeasureQuietForTests,
  resetScrollMeasureQuietForTests,
} from "../../platform/scrolling/themed-scrollbars.ts";
import { isShellLayoutBusy, resetShellLayoutBusyForTests } from "../../shell/shell-layout-busy.ts";
import {
  holdFilesEditorAttach,
  scheduleEditorChrome,
} from "./files-editor-attach.ts";

describe("files editor attach", () => {
  afterEach(() => {
    resetShellLayoutBusyForTests();
    resetScrollMeasureQuietForTests();
    vi.unstubAllGlobals();
  });

  it("quiets scrollbar measurement without taking the shell-resize token", () => {
    const host = document.createElement("div");
    const end = holdFilesEditorAttach(host);
    expect(isScrollMeasureQuietForTests()).toBe(true);
    expect(isShellLayoutBusy()).toBe(false);
    end();
    expect(isScrollMeasureQuietForTests()).toBe(false);
    end();
    expect(isScrollMeasureQuietForTests()).toBe(false);
  });

  it("applies chrome on this turn and releases the measure hold after first paint", () => {
    const host = document.createElement("div");
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      frames.push(cb);
      return frames.length;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);

    let applied = 0;
    const cancel = scheduleEditorChrome(() => {
      applied += 1;
    }, host);
    expect(applied).toBe(1);
    expect(isScrollMeasureQuietForTests()).toBe(true);
    expect(isShellLayoutBusy()).toBe(false);
    frames[0]?.(0);
    frames[1]?.(0);
    expect(isScrollMeasureQuietForTests()).toBe(false);
    cancel();
  });

  it("cancel before paint keeps chrome and still releases the hold", () => {
    const host = document.createElement("div");
    vi.stubGlobal("requestAnimationFrame", (_cb: FrameRequestCallback) => 1);
    vi.stubGlobal("cancelAnimationFrame", () => undefined);

    let applied = 0;
    const cancel = scheduleEditorChrome(() => {
      applied += 1;
    }, host);
    expect(applied).toBe(1);
    expect(isScrollMeasureQuietForTests()).toBe(true);
    cancel();
    expect(isScrollMeasureQuietForTests()).toBe(false);
    expect(isShellLayoutBusy()).toBe(false);
  });
});
