// @vitest-environment jsdom
import { batch, createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { DEFAULT_TRANSCRIPT_ORD_DENSITY_PX } from "./transcript-runway.ts";
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

function activitySpanRow(startOrd: number, count: number): TranscriptItem {
  return {
    kind: "activity_span",
    key: `span-${startOrd}`,
    label: "reading files",
    entries: Array.from({ length: count }, (_, i) => ({
      kind: "tool" as const,
      part: {
        id: `call-${startOrd + i}`,
        toolCallId: `call-${startOrd + i}`,
        assistantMessageId: `msg-${startOrd + i}`,
        messageId: `msg-${startOrd + i}`,
        tool: "read_file",
        kind: "read" as const,
        status: "completed" as const,
      },
    })),
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
        restoredRowEstimate: () => 100,
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

      // Prepend 100 older rows whose height matches the runway density (100px each):
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

  it("seeds runway density from restoredRowEstimate, ignores rowSizeHint estimates, and sums each measured row's real ordinal span", () => {
    const scroller = document.createElement("div");
    scroller.className = "den-chat-stream";
    Object.defineProperty(scroller, "clientHeight", { value: 500, configurable: true });
    document.body.appendChild(scroller);

    // 5 folded activity spans (3 messages each = 15 messages) and 8 prose rows (1 message each = 8 messages).
    const initialRows: TranscriptItem[] = [
      ...Array.from({ length: 5 }, (_, i) => activitySpanRow(255 + i * 3, 3)),
      ...Array.from({ length: 8 }, (_, i) => row(270 + i)),
    ];
    const [items, setItems] = createSignal<TranscriptItem[]>(initialRows);
    const [unloaded, setUnloaded] = createSignal({ before: 254, after: 0 });
    const [span, setSpan] = createSignal({ count: 13, ordSpan: 23 });
    const persisted = new Map<string, number>();

    const dispose = createRoot((d) => {
      // With only `rowSizeHint` estimates and no persisted or measured rows, density stays at 84px.
      const estimateOnly = createTranscriptVirtualizer({
        items,
        scrollElement: () => scroller,
        revealOffset: () => {},
        shiftContent: (deltaY) => deltaY,
        rowSizeHint: () => 200,
        unloadedOrds: unloaded,
        residentRowSpan: span,
      });
      expect(estimateOnly.runway().densityPxPerOrd).toBe(DEFAULT_TRANSCRIPT_ORD_DENSITY_PX);

      // Seed persisted heights for the 5 activity spans (60px each across 3 messages = 20px/ord).
      for (let i = 0; i < 5; i++) persisted.set(`span-${255 + i * 3}`, 60);

      const virtualizer = createTranscriptVirtualizer({
        items,
        scrollElement: () => scroller,
        revealOffset: () => {},
        shiftContent: (deltaY) => deltaY,
        rowSizeHint: (item) => persisted.get(item.key) ?? 200,
        restoredRowEstimate: (item) => persisted.get(item.key),
        unloadedOrds: unloaded,
        residentRowSpan: span,
      });

      // 5 persisted activity spans * 60px = 300px across 5 * 3 = 15 ordinals -> 20px/ord.
      // The 8 unmeasured prose rows with rowSizeHint=200 are excluded from density.
      expect(virtualizer.runway().densityPxPerOrd).toBe(20);

      // An older page lands with 2 prose rows measured at 90px each (1 ord each) in itemSizeCache
      // and 3 unmeasured activity spans (4 messages each).
      virtualizer.itemSizeCache.set("msg-241", 90);
      virtualizer.itemSizeCache.set("msg-242", 90);
      batch(() => {
        setUnloaded({ before: 240, after: 0 });
        setSpan({ count: 18, ordSpan: 37 });
        setItems([
          row(241),
          row(242),
          activitySpanRow(243, 4),
          activitySpanRow(247, 4),
          activitySpanRow(251, 4),
          ...initialRows,
        ]);
      });

      // Measured height = 2 * 90 + 5 * 60 = 480px across 2 * 1 + 5 * 3 = 17 ordinals.
      const expectedDensity = 480 / 17;
      expect(virtualizer.runway().densityPxPerOrd).toBeCloseTo(expectedDensity, 5);
      expect(virtualizer.runway().beforePx).toBe(Math.round(240 * expectedDensity));
      return d;
    });

    dispose();
    scroller.remove();
  });
});
