// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { mockScrollerMotion } from "../../../test/scroll-mock.ts";
import { resolveTranscriptItemAnchor } from "../projection/transcript-item-anchors.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import {
  createTranscriptVirtualizer,
  transcriptOffsetForPosition,
  type MeasuredTranscriptVirtualizer,
} from "./transcript-virtualizer.ts";

describe("createTranscriptVirtualizer lost anchor resolution", () => {
  const spanItem = (key: string, entryKeys: string[]): TranscriptItem => ({
    kind: "activity_span",
    key,
    label: "reading",
    entries: entryKeys.map((entry) => ({
      kind: "index_warming",
      key: entry,
      meta: {} as never,
    })),
  });

  it("carries the virtualizer reading anchor and refines open entry offsets from DOM measurements", async () => {
    const el = document.createElement("div");
    Object.defineProperty(el, "clientHeight", { value: 400, configurable: true });
    Object.defineProperty(el, "clientWidth", { value: 400, configurable: true });
    Object.defineProperty(el, "scrollHeight", { value: 100_000, configurable: true });
    Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
    mockScrollerMotion(el);
    document.body.appendChild(el);

    const user = (key: string): TranscriptItem => ({ kind: "user", key, text: key });
    const users = (prefix: string, count: number) =>
      Array.from({ length: count }, (_, i) => user(`${prefix}-${i}`));

    const [items, setItems] = createSignal<TranscriptItem[]>([
      ...users("u", 10),
      spanItem("call-b", ["call-b", "call-c"]),
      ...users("after", 20),
    ]);
    let open = true;
    let measuredDelta: number | undefined;
    const shifts: number[] = [];
    const resolveLostAnchor = (rowKey: string) => {
      const resolved = resolveTranscriptItemAnchor(items(), rowKey, () => open, 42);
      return resolved
        ? { ...resolved, measureOffsetPx: () => measuredDelta }
        : undefined;
    };
    let virtualizer!: MeasuredTranscriptVirtualizer;
    const dispose = createRoot((d) => {
      virtualizer = createTranscriptVirtualizer({
        items,
        scrollElement: () => el,
        resolveLostAnchor,
        rowSizeHint: (item) => (item.kind === "activity_span" ? 200 : undefined),
        revealOffset: () => {},
        shiftContent: (delta) => {
          shifts.push(delta);
          return delta;
        },
        observeElementRect: (_instance, cb) => {
          cb({ width: 400, height: 400 });
          return () => {};
        },
        observeElementOffset: (_instance, cb) => {
          cb(10 * 72 + 12, false);
          return () => {};
        },
      });
      return d;
    });

    try {
      expect(virtualizer.getVirtualItemForOffset(10 * 72 + 12)?.key).toBe("call-b");
      // DOM mounts after setOptions with prepended entry height 68px instead of the 42px estimate.
      setItems([
        ...users("x", 2),
        spanItem("call-a", ["call-a", "call-b", "call-c"]),
        ...users("after", 20),
      ]);
      measuredDelta = 68;
      await Promise.resolve();

      expect(virtualizer.getVirtualItemForOffset(virtualizer.scrollOffset!)?.key).toBe("call-a");
      expect(shifts).toEqual([2 * 72 - 10 * 72 + 68]);

      // When the row is not mounted at microtask time, the pending measurement is discarded
      // rather than carried over to the next row update.
      measuredDelta = undefined;
      setItems([
        ...users("x", 2),
        spanItem("call-0", ["call-0", "call-a", "call-b", "call-c"]),
        ...users("after", 20),
      ]);
      await Promise.resolve();
      expect(shifts.at(-1)).toBe(42);
      measuredDelta = 90;
      setItems([
        ...users("x", 3),
        spanItem("call-0", ["call-0", "call-a", "call-b", "call-c"]),
        ...users("after", 20),
      ]);
      await Promise.resolve();
      expect(shifts.at(-1)).toBe(72);

      open = false;
      expect(
        transcriptOffsetForPosition(
          virtualizer,
          { rowKey: "call-b", rowOffsetPx: 12 },
          resolveLostAnchor,
        ),
      ).toBe(3 * 72 + 12);
    } finally {
      dispose();
      el.remove();
    }
  });

  it("adjusts scroll offset when rows prepended into the before runway resize above the carried anchor", async () => {
    const el = document.createElement("div");
    Object.defineProperty(el, "clientHeight", { value: 400, configurable: true });
    Object.defineProperty(el, "clientWidth", { value: 400, configurable: true });
    Object.defineProperty(el, "scrollHeight", { value: 100_000, configurable: true });
    Object.defineProperty(el, "scrollTop", { value: 0, writable: true, configurable: true });
    mockScrollerMotion(el);
    document.body.appendChild(el);

    const user = (key: string): TranscriptItem => ({ kind: "user", key, text: key });
    const [items, setItems] = createSignal<TranscriptItem[]>([
      spanItem("call-b", ["call-b", "call-c"]),
      user("after-1"),
    ]);
    const [unloadedBefore, setUnloadedBefore] = createSignal(10);
    const shifts: number[] = [];
    let virtualizer!: MeasuredTranscriptVirtualizer;
    const dispose = createRoot((d) => {
      virtualizer = createTranscriptVirtualizer({
        items,
        scrollElement: () => el,
        unloadedOrds: () => ({ before: unloadedBefore(), after: 0 }),
        residentRowSpan: () => ({ count: items().length, ordSpan: items().length }),
        resolveLostAnchor: (rowKey) =>
          resolveTranscriptItemAnchor(items(), rowKey, () => true, 42),
        rowSizeHint: (item) => (item.kind === "activity_span" ? 200 : 72),
        revealOffset: () => {},
        shiftContent: (delta) => {
          shifts.push(delta);
          return delta;
        },
        observeElementRect: (_instance, cb) => {
          cb({ width: 400, height: 400 });
          return () => {};
        },
        observeElementOffset: (_instance, cb) => {
          cb(100, false);
          return () => {};
        },
      });
      return d;
    });

    try {
      expect(virtualizer.getVirtualItemForOffset(100)?.key).toBe("call-b");
      setUnloadedBefore(0);
      setItems([
        user("prepended-0"),
        user("prepended-1"),
        spanItem("call-a", ["call-a", "call-b", "call-c"]),
        user("after-1"),
      ]);
      await Promise.resolve();
      virtualizer.resizeItems([
        { index: 0, size: 60 },
        { index: 1, size: 64 },
      ]);
      expect(shifts.at(-1)).toBe(-20);
    } finally {
      dispose();
      el.remove();
    }
  });
});
