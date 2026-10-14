// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import {
  createTranscriptVirtualizer,
  transcriptUnmountedSpace,
} from "../layout/transcript-virtualizer.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";

function row(ord: number): TranscriptItem {
  return {
    kind: "assistant",
    key: `msg-${ord}`,
    text: `row ${ord}`,
  };
}

describe("createTranscriptVirtualizer session-scale runway", () => {
  it("sizes total extent from resident rows plus unloaded ordinal runways and shifts smoothly when older pages load", async () => {
    const scroller = document.createElement("div");
    scroller.className = "den-chat-stream";
    Object.defineProperty(scroller, "clientHeight", { value: 500, configurable: true });
    let scrollTop = 44_000;
    Object.defineProperty(scroller, "scrollTop", {
      get: () => scrollTop,
      set: (next: number) => {
        scrollTop = next;
      },
      configurable: true,
    });
    document.body.appendChild(scroller);

    const shifts: number[] = [];
    const [items, setItems] = createSignal<TranscriptItem[]>(
      Array.from({ length: 100 }, (_, i) => row(401 + i)),
    );
    const [unloaded, setUnloaded] = createSignal({ before: 400, after: 0 });
    const [span, setSpan] = createSignal({ count: 100, ordSpan: 100 });

    const dispose = createRoot((d) => {
      const virtualizer = createTranscriptVirtualizer({
        items,
        scrollElement: () => scroller,
        revealOffset: (offset) => {
          scrollTop = offset;
        },
        shiftContent: (deltaY) => {
          shifts.push(deltaY);
          scrollTop += deltaY;
          return deltaY;
        },
        rowSizeHint: () => 100,
        unloadedOrds: unloaded,
        residentRowSpan: span,
      });

      // 100 resident rows at 100px = 10_000px; 400 unloaded before at 100px/ord = 40_000px.
      expect(virtualizer.runway().beforePx).toBe(40_000);
      expect(virtualizer.getTotalSize()).toBe(50_000);
      expect(virtualizer.residentExtent()).toEqual({ startPx: 40_000, endPx: 50_000 });

      const unmounted = transcriptUnmountedSpace(
        virtualizer.getVirtualItems(),
        virtualizer.getTotalSize(),
        0,
      );
      expect(unmounted.beforePx).toBeGreaterThanOrEqual(40_000);

      // Prepend 100 older rows whose estimated height matches the runway density (100px each):
      // `beforePx` shrinks by 10_000px while 10_000px of resident rows are prepended above the anchor,
      // so the anchored row's session coordinate (`start`) stays at 44_000px with zero net jump!
      setUnloaded({ before: 300, after: 0 });
      setSpan({ count: 200, ordSpan: 200 });
      setItems(Array.from({ length: 200 }, (_, i) => row(301 + i)));

      expect(virtualizer.runway().beforePx).toBe(30_000);
      expect(virtualizer.getTotalSize()).toBe(50_000);
      expect(virtualizer.residentExtent()).toEqual({ startPx: 30_000, endPx: 50_000 });
      return d;
    });

    await Promise.resolve();
    expect(shifts).toEqual([]);
    dispose();
    scroller.remove();
  });
});
