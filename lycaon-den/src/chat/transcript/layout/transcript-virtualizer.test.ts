// @vitest-environment jsdom
import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import { singleFileEditFold } from "../../file-edit/file-edit-fold.ts";
import { bufferedVirtualRange } from "../../../layout/virtual-runway.ts";
import { flushScrollportFrameForTests, scheduleScrollportFrame } from "../../../platform/scrolling/scrollport-frame.ts";
import { bindScrollportMotion, unbindScrollportMotion } from "../../../platform/scrolling/scrollport-motion.ts";
import { subscribeStreamScroll } from "../../stream/stream-scroll.ts";
import { mockScrollerMotion } from "../../../test/scroll-mock.ts";
import {
  TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS,
  TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS,
  createTranscriptVirtualizer,
  type CreateTranscriptVirtualizerOptions,
  type TranscriptVirtualizer,
  transcriptInitialEndOffset,
  transcriptReadingPosition,
  transcriptOffsetForPosition,
  transcriptRowEstimatedHeight,
  transcriptVirtualRunway,
  transcriptViewportRect,
} from "./transcript-virtualizer.ts";

type TestVirtualizerOptions = Omit<
  CreateTranscriptVirtualizerOptions,
  "revealOffset" | "shiftContent"
> &
  Partial<Pick<CreateTranscriptVirtualizerOptions, "revealOffset" | "shiftContent">>;

function createTestTranscriptVirtualizer(opts: TestVirtualizerOptions) {
  return createTranscriptVirtualizer({
    ...opts,
    revealOffset: opts.revealOffset ?? (() => undefined),
    shiftContent: opts.shiftContent ?? (() => 0),
  });
}

function item(
  kind: TranscriptItem["kind"],
  key: string,
): TranscriptItem {
  switch (kind) {
    case "user":
      return { kind, key, text: key };
    case "assistant":
      return { kind, key, text: key };
    case "activity_span":
      return {
        kind,
        key,
        label: key,
        entries: [],
      };
    case "worker_group":
      return { kind, key, parts: [] };
    case "worker_file_edit":
      return {
        kind,
        key,
        anchorMessageId: key,
        folds: [
          singleFileEditFold("m1", fileEditPreviewFixture({ path: "a.ts", before: "", after: "x" })),
        ],
        ts: "t",
      };
    case "tool":
      return {
        kind,
        key,
        part: {
          id: key,
          toolCallId: key,
          assistantMessageId: "assistant-message",
          messageId: key,
          tool: "command",
          kind: "command",
          status: "completed",
        },
      };
    default:
      return { kind: "user", key, text: key };
  }
}

function mockScrollEl(height: number): HTMLElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientHeight", {
    value: height,
    configurable: true,
  });
  Object.defineProperty(el, "clientWidth", {
    value: 400,
    configurable: true,
  });
  Object.defineProperty(el, "scrollHeight", {
    value: height * 40,
    configurable: true,
  });
  Object.defineProperty(el, "scrollTop", {
    value: 0,
    writable: true,
    configurable: true,
  });
  mockScrollerMotion(el);
  document.body.appendChild(el);
  return el;
}

describe("transcriptRowEstimatedHeight", () => {
  it("seeds per-kind estimate priors", () => {
    expect(transcriptRowEstimatedHeight({ kind: "worker_file_edit" })).toBe(120);
    expect(transcriptRowEstimatedHeight({ kind: "activity_span" })).toBe(64);
    expect(transcriptRowEstimatedHeight({ kind: "worker_group" })).toBe(136);
    expect(transcriptRowEstimatedHeight({ kind: "tool" })).toBe(72);
    expect(transcriptRowEstimatedHeight({ kind: "assistant" })).toBe(72);
    expect(transcriptRowEstimatedHeight({ kind: "user" })).toBe(72);
    expect(transcriptRowEstimatedHeight({ kind: "workflow_feedback" })).toBe(40);
    expect(transcriptRowEstimatedHeight({ kind: "checkpoint" })).toBe(40);
    expect(transcriptRowEstimatedHeight({ kind: "workflow_boundary" })).toBe(32);
  });

  it("keeps the kind prior when live prose grows", () => {
    expect(transcriptRowEstimatedHeight({ kind: "assistant" })).toBe(72);
    expect(transcriptRowEstimatedHeight({ kind: "draft" })).toBe(72);
    expect(transcriptRowEstimatedHeight({ kind: "blueprint_card" })).toBe(160);
    expect(transcriptRowEstimatedHeight({ kind: "user" })).toBe(72);
  });
});

function jsdomObservers(height: number, width = 400, offset = 0) {
  return {
    rowSizeHint: () => undefined,
    observeElementRect: (
      _instance: unknown,
      cb: (rect: { width: number; height: number }) => void,
    ) => {
      cb({ width, height });
      return () => {};
    },
    observeElementOffset: (
      _instance: unknown,
      cb: (offset: number, isScrolling: boolean) => void,
    ) => {
      cb(offset, false);
      return () => {};
    },
  };
}

function uniformWindowRowBound(viewportHeight: number, rowHeight: number) {
  const rowExtent = rowHeight;
  const visibleRows = Math.ceil(viewportHeight / rowExtent) + 1;
  const bufferRows = Math.max(
    TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS,
    Math.ceil(
      (viewportHeight * TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS) / rowExtent,
    ),
  );
  return visibleRows + 2 * bufferRows;
}

describe("transcript virtual runway", () => {
  it("projects unmounted space around a normal-flow window", () => {
    expect(
      transcriptVirtualRunway(
        [
          { start: 240, end: 320 },
          { start: 334, end: 434 },
        ],
        1_000,
      ),
    ).toEqual({ beforePx: 240, afterPx: 566 });
  });

  it("clamps transient runway geometry", () => {
    expect(
      transcriptVirtualRunway(
        [{ start: Number.NaN, end: Number.POSITIVE_INFINITY }],
        Number.NaN,
      ),
    ).toEqual({ beforePx: 0, afterPx: 0 });
    expect(transcriptVirtualRunway([], 400)).toEqual({
      beforePx: 0,
      afterPx: 400,
    });
  });

  it("fills a viewport-sized runway around dense rows", () => {
    const indexes = bufferedVirtualRange({
      range: { startIndex: 100, endIndex: 110, overscan: 6, count: 500 },
      bufferPx: 400,
      rowSize: () => 25,
    });

    expect(indexes[0]).toBe(84);
    expect(indexes[indexes.length - 1]).toBe(126);
    expect((100 - indexes[0]!) * 25).toBeGreaterThanOrEqual(400);
    expect((indexes[indexes.length - 1]! - 110) * 25).toBeGreaterThanOrEqual(400);
  });

  it("retains a minimum item runway around very tall rows", () => {
    const indexes = bufferedVirtualRange({
      range: { startIndex: 10, endIndex: 12, overscan: 6, count: 30 },
      bufferPx: 400,
      rowSize: () => 1_000,
    });

    expect(indexes).toEqual(Array.from({ length: 15 }, (_, index) => index + 4));
  });

  it("stops cleanly at transcript boundaries", () => {
    const indexes = bufferedVirtualRange({
      range: { startIndex: 0, endIndex: 1, overscan: 6, count: 4 },
      bufferPx: 900,
      rowSize: () => 25,
    });

    expect(indexes).toEqual([0, 1, 2, 3]);
  });
});

describe("transcriptViewportRect", () => {
  it("uses the scrollport client box instead of its smaller observed content box", () => {
    expect(transcriptViewportRect({ width: 300, height: 439, borderWidth: 400, borderHeight: 600 })).toEqual({
      width: 400,
      height: 600,
    });
  });

  it("falls back to rounded observed geometry before client layout exists", () => {
    expect(
      transcriptViewportRect({ width: 399.6, height: 438.7 }),
    ).toEqual({ width: 400, height: 439 });
  });
});

describe("transcriptInitialEndOffset", () => {
  it("uses the shared scroll end for a chat span", () => {
    const scrollEl = mockScrollEl(600);
    scrollEl.className = "den-chat-stream";
    Object.defineProperty(scrollEl, "scrollHeight", {
      value: 4_000,
      configurable: true,
    });

    expect(transcriptInitialEndOffset(scrollEl, 900)).toBe(3_400);
  });

  it("uses the local end outside the shared chat scrollport", () => {
    const scrollEl = mockScrollEl(600);
    expect(transcriptInitialEndOffset(scrollEl, 900)).toBe(900);
  });
});

describe("createTranscriptVirtualizer", () => {
  const disposers: Array<() => void> = [];
  afterEach(() => {
    vi.restoreAllMocks();
    while (disposers.length) disposers.pop()?.();
    document.body.replaceChildren();
  });

  it("preserves the space above a host anchor when the reader is on its date label", () => {
    const scrollEl = mockScrollEl(400);
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => [
          { kind: "time_marker", key: "day", variant: "day", at: 1, anchorMessageId: "u0" },
          item("user", "u0"),
        ],
        scrollElement: () => scrollEl,
        ...jsdomObservers(400),
      });
      virtualizer.scrollOffset = 0;
      virtualizer.scrollElement = scrollEl;
      const position = transcriptReadingPosition(virtualizer, (key) => key !== "day");
      expect(position).toEqual({ rowKey: "u0", rowOffsetPx: -24 });
      expect(transcriptOffsetForPosition(virtualizer, position!)).toBe(0);
    });
  });

  it("bounds the windowed range by viewport plus its painted runway", () => {
    const scrollEl = mockScrollEl(400);
    const items = Array.from({ length: 500 }, (_, i) =>
      item("user", `u-${i}`),
    );
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400),
      });
      const virtual = virtualizer.getVirtualItems();
      expect(virtual.length).toBeGreaterThan(0);
      expect(virtual.length).toBeLessThanOrEqual(uniformWindowRowBound(400, 72));
      // Seams live inside the rows, so the extent is the sum of row sizes.
      expect(virtualizer.getTotalSize()).toBe(500 * 72);
    });
  });

  it("uses local virtual coordinates without live scroll margin feedback", async () => {
    const scrollEl = mockScrollEl(400);
    scrollEl.className = "den-chat-stream";
    scrollEl.scrollTop = 200;
    const content = document.createElement("section");
    content.className = "den-chat-stream-inner";
    scrollEl.appendChild(content);
    const items = [item("user", "u0"), item("assistant", "a1")];

    let virtualizer: ReturnType<typeof createTranscriptVirtualizer>;
    createRoot((dispose) => {
      disposers.push(dispose);
      virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400, 400, 600),
      });
      const row = document.createElement("div");
      row.setAttribute("data-index", "0");
      Object.defineProperty(row, "offsetHeight", {
        value: 72,
        configurable: true,
      });
      content.appendChild(row);
      virtualizer.measureElement(row);
    });
    await Promise.resolve();

    const [first, second] = virtualizer!.getVirtualItems();
    expect(first?.start).toBe(0);
    expect(second?.start).toBe(72);
    expect(virtualizer!.getTotalSize()).toBe(2 * 72);
    expect(virtualizer!.getOffsetForIndex(0, "start")?.[0]).toBe(0);
  });

  it("carries the offset only for rows wholly above the reading offset", () => {
    const scrollEl = mockScrollEl(400);
    const items = [item("user", "u0"), item("assistant", "a1")];
    let virtualizer!: ReturnType<typeof createTranscriptVirtualizer>;
    createRoot((dispose) => {
      disposers.push(dispose);
      virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400, 400, 600),
      });
    });
    const adjusts = (start: number, end: number) =>
      virtualizer.shouldAdjustScrollPositionOnItemSizeChange?.(
        { key: "u0", index: 0, start, end, size: end - start, lane: 0 },
        10,
        virtualizer,
      );

    expect(adjusts(0, 600)).toBe(true);
    expect(adjusts(550, 650)).toBe(false);
    expect(adjusts(650, 700)).toBe(false);
  });

  it("does not end-anchor a growing row when later spans extend the stream", async () => {
    const scrollEl = mockScrollEl(400);
    scrollEl.className = "den-chat-stream";
    scrollEl.getBoundingClientRect = () => ({ top: 100 }) as DOMRect;
    const scrollTo = vi.fn();
    scrollEl.scrollTo = scrollTo;
    const body = document.createElement("div");
    body.className = "den-chat-stream-body";
    body.getBoundingClientRect = () => ({ bottom: 1_600 }) as DOMRect;
    const content = document.createElement("section");
    content.className = "den-chat-stream-inner";
    content.getBoundingClientRect = () =>
      ({ top: 720, bottom: 878 }) as DOMRect;
    body.appendChild(content);
    scrollEl.appendChild(body);
    const items = [item("assistant", "a0"), item("user", "u1")];

    let virtualizer: ReturnType<typeof createTranscriptVirtualizer>;
    createRoot((dispose) => {
      disposers.push(dispose);
      virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400, 400, 640),
      });
      const row = document.createElement("div");
      row.setAttribute("data-index", "0");
      content.appendChild(row);
      virtualizer.options.measureElement(row, undefined, virtualizer);
    });
    await Promise.resolve();

    virtualizer!.resizeItem(0, 80);
    scrollTo.mockClear();
    virtualizer!.resizeItem(0, 100);

    expect(scrollTo).not.toHaveBeenCalled();
  });

  it("keeps a full viewport mounted beyond dense visible rows", () => {
    const viewportHeight = 400;
    const scrollOffset = 5_000;
    const scrollEl = mockScrollEl(viewportHeight);
    const items = Array.from({ length: 500 }, (_, i) =>
      item("tool", `tool-${i}`),
    );
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(viewportHeight, 400, scrollOffset),
        rowSizeHint: () => 25,
      });
      const virtual = virtualizer.getVirtualItems();
      const first = virtual[0];
      const last = virtual[virtual.length - 1];

      expect(first).toBeDefined();
      expect(last).toBeDefined();
      expect(first!.start).toBeLessThanOrEqual(scrollOffset - viewportHeight);
      expect(last!.end).toBeGreaterThanOrEqual(
        scrollOffset + 2 * viewportHeight,
      );
      expect(virtual.length).toBeLessThanOrEqual(
        uniformWindowRowBound(viewportHeight, 25),
      );
    });
  });

  it("recalculates the runway when mounted rows measure below their estimates", async () => {
    const scrollEl = mockScrollEl(400);
    const items = Array.from({ length: 100 }, (_, i) =>
      item("tool", `tool-${i}`),
    );
    let virtualizer: ReturnType<typeof createTranscriptVirtualizer>;
    let initialExtractor: ReturnType<
      typeof createTranscriptVirtualizer
    >["options"]["rangeExtractor"];
    let initialRowCount = 0;
    createRoot((dispose) => {
      disposers.push(dispose);
      virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400),
      });
      initialExtractor = virtualizer.options.rangeExtractor;
      const initialRows = [...virtualizer.getVirtualItems()];
      initialRowCount = initialRows.length;

      for (const virtualRow of initialRows) {
        const row = document.createElement("div");
        row.setAttribute("data-index", String(virtualRow.index));
        Object.defineProperty(row, "offsetHeight", {
          value: 25,
          configurable: true,
        });
        scrollEl.appendChild(row);
        virtualizer.measureElement(row);
      }
    });

    await Promise.resolve();
    expect(virtualizer!.options.rangeExtractor).not.toBe(initialExtractor!);
    expect(virtualizer!.getVirtualItems().length).toBeGreaterThan(
      initialRowCount,
    );
  });

  it("keeps range extraction free of scheduled work", async () => {
    const scrollEl = mockScrollEl(400);
    const items = Array.from({ length: 20 }, (_, i) => item("user", `u-${i}`));
    let virtualizer: ReturnType<typeof createTranscriptVirtualizer>;
    createRoot((dispose) => {
      disposers.push(dispose);
      virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400),
      });
    });
    await Promise.resolve();
    const queue = vi.spyOn(globalThis, "queueMicrotask");

    virtualizer!.options.rangeExtractor({
      startIndex: 0,
      endIndex: 5,
      overscan: TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS,
      count: items.length,
    });

    expect(queue).not.toHaveBeenCalled();
    queue.mockRestore();
  });

  it("seeds the first chat range at the estimated transcript end", () => {
    const scrollEl = mockScrollEl(400);
    const items = Array.from({ length: 500 }, (_, i) =>
      item("user", `u-${i}`),
    );
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400),
      });
      const initialOffset = virtualizer.options.initialOffset;
      expect(typeof initialOffset).toBe("function");
      expect((initialOffset as () => number)()).toBe(
        500 * 72 - 400,
      );
      expect(virtualizer.options.followOnAppend).toBe(false);
      expect(virtualizer.options.anchorTo).toBe("start");
    });
  });

  it("does not chase the tail when the last visible row expands", () => {
    const scrollEl = mockScrollEl(300);
    const scrollTo = vi.fn();
    scrollEl.scrollTo = scrollTo;
    const items = [item("assistant", "a1"), item("activity_span", "tools")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300, 400, 0),
      });
      scrollTo.mockClear();

      // Animated tail-row measurements update geometry without scrolling.
      virtualizer.resizeItem(1, 120);
      virtualizer.resizeItem(1, 220);
      virtualizer.resizeItem(1, 320);
    });

    expect(scrollTo).not.toHaveBeenCalled();
  });

  it("updates total size when measureElement overrides an estimate", () => {
    const scrollEl = mockScrollEl(300);
    const items = [item("assistant", "a1"), item("user", "u1")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      const before = virtualizer.getTotalSize();
      expect(before).toBe(72 + 72);
      virtualizer.resizeItem(0, 240);
      expect(
        virtualizer.itemSizeCache.get(virtualizer.options.getItemKey(0)),
      ).toBe(240);
      expect(virtualizer.getTotalSize()).toBe(240 + 72);
    });
  });

  it("uses persisted size hints for its first reload placement", () => {
    const scrollEl = mockScrollEl(300);
    const items = [item("activity_span", "visuals"), item("assistant", "summary")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
        rowSizeHint: (row) =>
          row.key === "visuals" ? 353 : row.key === "summary" ? 240 : undefined,
      });

      const [visuals, summary] = virtualizer.getVirtualItems();
      expect(visuals?.size).toBe(353);
      expect(summary?.start).toBe(353);
      expect(virtualizer.getTotalSize()).toBe(353 + 240);
    });
  });

  it("uses presentation size hints when no persisted height exists", () => {
    const scrollEl = mockScrollEl(300);
    const items = [item("tool", "expanded"), item("assistant", "summary")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
        rowSizeHint: (row) => (row.key === "expanded" ? 420 : undefined),
      });

      const [expanded, summary] = virtualizer.getVirtualItems();
      expect(expanded?.size).toBe(420);
      expect(summary?.start).toBe(420);
    });
  });

  it("leaves consecutive rows flush, because a seam sits inside the row below it", () => {
    const scrollEl = mockScrollEl(400);
    const items = [item("user", "u0"), item("assistant", "a1")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(400),
      });
      const [first, second] = virtualizer.getVirtualItems();
      expect(first).toBeDefined();
      expect(second).toBeDefined();
      expect(second!.start - first!.end).toBe(0);
    });
  });

  it("declines a zero-height ResizeObserver entry", () => {
    // Transient zero measurements preserve the settled size.
    const scrollEl = mockScrollEl(300);
    const items = [item("assistant", "a1"), item("user", "u1")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      virtualizer.resizeItem(0, 240);
      expect(virtualizer.getTotalSize()).toBe(240 + 72);

      const row = document.createElement("div");
      row.setAttribute("data-index", "0");
      const blind = {
        borderBoxSize: [{ blockSize: 0, inlineSize: 0 }],
      } as unknown as ResizeObserverEntry;
      expect(
        virtualizer.options.measureElement(row, blind, virtualizer),
      ).toBe(240);

      // An unmeasured row uses its kind estimate.
      const second = document.createElement("div");
      second.setAttribute("data-index", "1");
      expect(
        virtualizer.options.measureElement(second, blind, virtualizer),
      ).toBe(transcriptRowEstimatedHeight({ kind: "user" }));
    });
  });

  it.each([39.125, 40.5, 41.875])("preserves fractional row heights across a virtual window (%s px)", (height) => {
    const scrollEl = mockScrollEl(300);
    const items = Array.from({ length: 20 }, (_, index) => item("assistant", `fraction-${index}`));
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      for (let index = 0; index < items.length; index += 1) {
        const row = document.createElement("div");
        row.setAttribute("data-index", String(index));
        const entry = { borderBoxSize: [{ blockSize: height, inlineSize: 300 }] } as unknown as ResizeObserverEntry;
        const size = virtualizer.options.measureElement(row, entry, virtualizer);
        expect(size).toBe(height);
        virtualizer.resizeItem(index, size);
      }
      expect(virtualizer.getTotalSize()).toBe(height * items.length);
    });
  });

  it("replaces a stale size hint with one atomic live measurement", () => {
    const scrollEl = mockScrollEl(300);
    const items = [item("assistant", "a1")];
    const onRowMeasured = vi.fn();
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
        rowSizeHint: () => 800,
        onRowMeasured,
      });
      expect(virtualizer.getVirtualItems()[0]?.size).toBe(800);
      const row = document.createElement("div");
      row.setAttribute("data-index", "0");
      Object.defineProperty(row, "offsetHeight", {
        value: 40,
        configurable: true,
      });
      virtualizer.measureElement(row);

      expect(
        virtualizer.itemSizeCache.get(virtualizer.options.getItemKey(0)),
      ).toBe(40);
      expect(onRowMeasured).toHaveBeenCalledOnce();
      expect(onRowMeasured).toHaveBeenCalledWith({ element: row, index: 0, size: 40, width: 0 });
    });
  });

  it("re-reads the DOM on an explicit measure of an already-measured row", () => {
    // Explicit measurement always reads live geometry.
    const scrollEl = mockScrollEl(300);
    const items = [item("assistant", "a1")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      virtualizer.resizeItem(0, 240);
      const row = document.createElement("div");
      row.setAttribute("data-index", "0");
      Object.defineProperty(row, "offsetHeight", {
        value: 310,
        configurable: true,
      });
      expect(
        virtualizer.options.measureElement(row, undefined, virtualizer),
      ).toBe(310);
    });
  });

  it("measures an animating row at its live height", () => {
    const scrollEl = mockScrollEl(300);
    const items = [item("activity_span", "tools")];
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      const row = document.createElement("div");
      row.setAttribute("data-index", "0");
      const details = document.createElement("details");
      details.dataset.animating = "true";
      row.append(details);
      let height = 40;
      Object.defineProperty(row, "offsetHeight", {
        get: () => height,
        configurable: true,
      });

      expect(
        virtualizer.options.measureElement(row, undefined, virtualizer),
      ).toBe(40);
      height = 90;
      expect(
        virtualizer.options.measureElement(row, undefined, virtualizer),
      ).toBe(90);
    });
  });

  it("resolves each layout's keys from the items it was built from", () => {
    const scrollEl = mockScrollEl(300);
    const [items, setItems] = createSignal<TranscriptItem[]>([
      item("user", "a"),
      item("assistant", "b"),
      item("tool", "c"),
    ]);
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      const layoutKey = virtualizer.options.getItemKey;
      expect([0, 1, 2].map(layoutKey)).toEqual(["a", "b", "c"]);

      setItems([items()[2]!, items()[0]!, items()[1]!]);

      expect([0, 1, 2].map(layoutKey)).toEqual(["a", "b", "c"]);
      expect([0, 1, 2].map((i) => virtualizer.options.getItemKey(i))).toEqual([
        "c",
        "a",
        "b",
      ]);
    });
  });

  it("mints a new getItemKey identity when a same-count substitution changes keys", () => {
    const scrollEl = mockScrollEl(300);
    const [items, setItems] = createSignal<TranscriptItem[]>([
      item("user", "a"),
      item("user", "b"),
    ]);
    createRoot((dispose) => {
      disposers.push(dispose);
      const virtualizer = createTestTranscriptVirtualizer({
        items,
        scrollElement: () => scrollEl,
        ...jsdomObservers(300),
      });
      // A new key function invalidates measurements even when the count is unchanged.
      const keyFnBefore = virtualizer.options.getItemKey;
      setItems([items()[0]!, item("assistant", "c")]);
      expect(virtualizer.options.getItemKey).not.toBe(keyFnBefore);
      expect(virtualizer.options.getItemKey(1)).toBe("c");
      expect(
        virtualizer.getVirtualItems().map((row) => String(row.key)),
      ).toEqual(["a", "c"]);
      // An identical rebuild keeps the identity, so nothing re-measures.
      const keyFnAfter = virtualizer.options.getItemKey;
      setItems([...items()]);
      expect(virtualizer.options.getItemKey).toBe(keyFnAfter);
    });
  });

  describe("reading row anchoring", () => {
    const rowExtent = 72;

    function users(prefix: string, count: number): TranscriptItem[] {
      return Array.from({ length: count }, (_, i) =>
        item("user", `${prefix}-${i}`),
      );
    }

    function anchoredVirtualizer(initial: TranscriptItem[], offset: number) {
      const scrollEl = mockScrollEl(400);
      const [items, setItems] = createSignal(initial);
      const shifts: number[] = [];
      let virtualizer!: ReturnType<typeof createTranscriptVirtualizer>;
      createRoot((dispose) => {
        disposers.push(dispose);
        virtualizer = createTestTranscriptVirtualizer({
          items,
          scrollElement: () => scrollEl,
          ...jsdomObservers(400, 400, offset),
          shiftContent: (deltaY) => {
            shifts.push(deltaY);
            return deltaY;
          },
        });
      });
      expect(virtualizer.scrollOffset).toBe(offset);
      return { virtualizer, items, setItems, shifts };
    }

    it("keeps the reading row when older rows are prepended", async () => {
      const { virtualizer, items, setItems, shifts } = anchoredVirtualizer(
        users("u", 40),
        1_000,
      );
      const reading = virtualizer.getVirtualItemForOffset(1_000)?.key;

      setItems([...users("old", 5), ...items()]);

      // The range follows at once; the scrollport once the rows have painted.
      expect(virtualizer.scrollOffset).toBe(1_000 + 5 * rowExtent);
      expect(
        virtualizer.getVirtualItemForOffset(virtualizer.scrollOffset!)?.key,
      ).toBe(reading);
      expect(shifts).toEqual([]);
      await Promise.resolve();
      expect(shifts).toEqual([5 * rowExtent]);
    });

    it("keeps the reading row when rows above it are evicted", async () => {
      const { virtualizer, items, setItems, shifts } = anchoredVirtualizer(
        users("u", 40),
        1_000,
      );

      setItems(items().slice(3));
      await Promise.resolve();

      expect(shifts).toEqual([-3 * rowExtent]);
      expect(virtualizer.scrollOffset).toBe(1_000 - 3 * rowExtent);
    });

    it("carries successive row changes in one scrollport move", async () => {
      const { virtualizer, items, setItems, shifts } = anchoredVirtualizer(
        users("u", 40),
        1_000,
      );

      setItems([...users("old", 2), ...items()]);
      setItems([...users("older", 3), ...items()]);
      await Promise.resolve();

      expect(shifts).toEqual([5 * rowExtent]);
      expect(virtualizer.scrollOffset).toBe(1_000 + 5 * rowExtent);
    });

    it("keeps the virtual offset on what the scrollport could apply", async () => {
      const scrollEl = mockScrollEl(400);
      const [items, setItems] = createSignal(users("u", 40));
      let virtualizer!: ReturnType<typeof createTranscriptVirtualizer>;
      createRoot((dispose) => {
        disposers.push(dispose);
        virtualizer = createTestTranscriptVirtualizer({
          items,
          scrollElement: () => scrollEl,
          ...jsdomObservers(400, 400, 1_000),
          shiftContent: (deltaY) => Math.min(deltaY, 100),
        });
      });

      setItems([...users("old", 5), ...items()]);
      await Promise.resolve();

      expect(virtualizer.scrollOffset).toBe(1_100);
    });

    it("leaves the offset alone for rows appended below", () => {
      const { items, setItems, shifts } = anchoredVirtualizer(
        users("u", 40),
        1_000,
      );

      setItems([...items(), ...users("new", 5)]);

      expect(shifts).toEqual([]);
    });

    it("does not carry an offset into an unrelated transcript", () => {
      const { setItems, shifts } = anchoredVirtualizer(users("u", 40), 1_000);

      setItems(users("other", 40));

      expect(shifts).toEqual([]);
    });

    it("applies above-fold measurement corrections as content shifts", () => {
      const scrollEl = mockScrollEl(400);
      const rows = users("u", 40);
      const shifts: number[] = [];
      const reveals: number[] = [];
      createRoot((dispose) => {
        disposers.push(dispose);
        const virtualizer = createTestTranscriptVirtualizer({
          items: () => rows,
          scrollElement: () => scrollEl,
          ...jsdomObservers(400, 400, 1_000),
          shiftContent: (deltaY) => {
            shifts.push(deltaY);
            return deltaY;
          },
          revealOffset: (offset) => {
            reveals.push(offset);
          },
        });
        expect(reveals).toEqual([1_000]);
        expect(shifts).toEqual([]);

        virtualizer.resizeItem(0, 172);
      });

      expect(shifts).toEqual([100]);
      expect(reveals).toEqual([1_000]);
    });
  });
});

it.each([false, true])("reads an arriving window before applying sizes while scrolling=%s", (scrolling) => {
  const scrollEl = mockScrollEl(300);
  const items = [item("assistant", "first"), item("assistant", "second")];
  createRoot((dispose) => {
    try {
      const trace: string[] = [];
      const virtualizer = createTestTranscriptVirtualizer({ items: () => items, scrollElement: () => scrollEl, ...jsdomObservers(300) });
      const rows = items.map((_, index) => {
        const row = document.createElement("div");
        row.dataset.index = String(index);
        row.getBoundingClientRect = () => { trace.push(`read-${index}`); return { height: 150 + index } as DOMRect; };
        scrollEl.append(row);
        return row;
      });
      const resize = virtualizer.resizeItem;
      virtualizer.resizeItem = (index, size) => { trace.push(`resize-${index}`); resize(index, size); };
      virtualizer.isScrolling = scrolling;
      rows.forEach(virtualizer.queueMeasurement);
      flushScrollportFrameForTests(scrollEl);
      expect(trace).toEqual(["read-0", "read-1", "resize-0", "resize-1"]);
      expect(virtualizer.getTotalSize()).toBe(301);
    } finally { dispose(); scrollEl.remove(); }
  });
});

it("combines above-reader row measurements into one physical content shift", () => {
  const element = mockScrollEl(300);
  const shifts: number[] = [];
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 40 }, (_, index) => item("user", `batch-${index}`)),
        scrollElement: () => element,
        shiftContent: (delta) => { shifts.push(delta); return delta; },
        ...jsdomObservers(300),
      });
      virtualizer.scrollOffset = 1000;
      virtualizer.scrollElement = element;
      virtualizer.resizeItems([{ index: 0, size: 100 }, { index: 1, size: 120 }, { index: 2, size: 90 }]);
      expect(shifts).toEqual([94]);
      expect(virtualizer.scrollOffset).toBe(1094);
    } finally { dispose(); }
  });
});

it("keeps virtual geometry on the correction the scrollport actually applies", () => {
  const element = mockScrollEl(300);
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 40 }, (_, index) => item("user", `limit-${index}`)),
        scrollElement: () => element,
        shiftContent: () => 20,
        ...jsdomObservers(300),
      });
      virtualizer.scrollOffset = 1000;
      virtualizer.scrollElement = element;
      virtualizer.resizeItems([{ index: 0, size: 100 }, { index: 1, size: 120 }]);
      expect(virtualizer.scrollOffset).toBe(1020);
    } finally { dispose(); }
  });
});

it("refreshes unmeasured placement estimates while retaining measured rows and the reading anchor", async () => {
  const element = mockScrollEl(300);
  const [inset, setInset] = createSignal(0);
  let dispose!: () => void;
  const virtualizer = createRoot((stop) => {
    dispose = stop;
    return createTestTranscriptVirtualizer({
      items: () => Array.from({ length: 40 }, (_, index) => item("user", `inset-${index}`)),
      scrollElement: () => element,
      ...jsdomObservers(300),
      rowSizeHint: () => 72 + inset(),
      placementBasis: inset,
      shiftContent: (delta) => delta,
    });
  });
  try {
    virtualizer.resizeItem(0, 100);
    virtualizer.scrollOffset = 1000;
    virtualizer.scrollElement = element;
    const position = transcriptReadingPosition(virtualizer);
    setInset(12.125);
    await Promise.resolve();
    expect(virtualizer.itemSizeCache.get("inset-0")).toBe(100);
    expect(virtualizer.measurementsCache[1]?.size).toBe(84.125);
    expect(transcriptReadingPosition(virtualizer)).toEqual(position);
  } finally { dispose(); }
});

it("re-estimates only unmounted rows on a geometry change and keeps measured rows in place", async () => {
  const element = mockScrollEl(300);
  const [basis, setBasis] = createSignal("narrow");
  const shifts: number[] = [];
  let dispose!: () => void;
  const virtualizer = createRoot((stop) => {
    dispose = stop;
    return createTestTranscriptVirtualizer({
      items: () => Array.from({ length: 40 }, (_, index) => item("user", `geo-${index}`)),
      scrollElement: () => element,
      ...jsdomObservers(300, 400, 1_000),
      rowSizeHint: () => (basis() === "narrow" ? 72 : 82),
      estimateBasis: basis,
      shiftContent: (delta) => { shifts.push(delta); return delta; },
    });
  });
  try {
    const row = document.createElement("div");
    row.setAttribute("data-index", "3");
    Object.defineProperty(row, "offsetHeight", { value: 100, configurable: true });
    document.body.appendChild(row);
    virtualizer.measureElement(row);
    expect(virtualizer.itemSizeCache.get("geo-3")).toBe(100);
    virtualizer.scrollOffset = 1_000;
    const position = transcriptReadingPosition(virtualizer);
    expect(position).toEqual({ rowKey: "geo-13", rowOffsetPx: 36 });
    shifts.length = 0;

    setBasis("wide");
    await Promise.resolve();

    // The mounted row keeps its measurement until the remeasure at the new width lands.
    expect(virtualizer.itemSizeCache.get("geo-3")).toBe(100);
    expect(virtualizer.measurementsCache[4]?.size).toBe(82);
    // The reader moved once, by the estimate change of the unmounted rows above them.
    expect(shifts).toEqual([120]);
    expect(virtualizer.scrollOffset).toBe(1_120);
    expect(transcriptReadingPosition(virtualizer)).toEqual(position);
  } finally { dispose(); }
});

it("resolves the captured reading offset before deferred virtual rendering catches up", () => {
  const element = mockScrollEl(300);
  const motion = bindScrollportMotion(element, element, element);
  let stop = () => {};
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 40 }, (_, index) => item("user", `read-${index}`)),
        scrollElement: () => element,
        observeElementRect: jsdomObservers(300).observeElementRect,
      });
      let position: ReturnType<typeof transcriptReadingPosition> = null;
      let renderedOffset: number | null = null;
      stop = subscribeStreamScroll(element, () => {
        renderedOffset = virtualizer.scrollOffset;
        position = transcriptReadingPosition(virtualizer, undefined, motion.offsetY());
      });
      element.scrollTop = 1000;
      element.dispatchEvent(new Event("scroll"));
      flushScrollportFrameForTests(element);
      expect(position).toEqual({ rowKey: "read-13", rowOffsetPx: 64 });
      expect(renderedOffset).not.toBe(1000);
    } finally { stop(); dispose(); unbindScrollportMotion(element); element.remove(); }
  });
});


it("keeps one reading anchor while a batch contracts rows above it", () => {
  const element = mockScrollEl(300);
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 20 }, (_, index) => item("user", `contract-${index}`)),
        scrollElement: () => element,
        ...jsdomObservers(300, 400, 250),
        rowSizeHint: () => 100,
        shiftContent: (delta) => delta,
      });
      const position = transcriptReadingPosition(virtualizer);
      virtualizer.resizeItems([{ index: 0, size: 20 }, { index: 1, size: 20 }]);
      expect(virtualizer.scrollOffset).toBe(90);
      expect(transcriptReadingPosition(virtualizer)).toEqual(position);
    } finally { dispose(); }
  });
});

it("publishes derived height hints after every row in a measurement batch is applied", () => {
  const element = mockScrollEl(300);
  document.body.append(element);
  const hints = new Map<string, number>();
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 20 }, (_, index) => item("user", `cache-${index}`)),
        scrollElement: () => element,
        ...jsdomObservers(300, 400, 250),
        rowSizeHint: (item) => hints.get(item.key) ?? 100,
        shiftContent: (delta) => delta,
        onRowMeasured: ({ index, size }) => hints.set(`cache-${index}`, size),
      });
      const position = transcriptReadingPosition(virtualizer);
      for (const index of [0, 1]) {
        const row = document.createElement("div");
        row.dataset.index = String(index);
        Object.defineProperty(row, "offsetHeight", { value: 20 });
        element.append(row);
        virtualizer.queueMeasurement(row);
      }
      flushScrollportFrameForTests(element);
      expect(virtualizer.scrollOffset).toBe(90);
      expect(transcriptReadingPosition(virtualizer)).toEqual(position);
      expect([...hints.values()]).toEqual([20, 20]);
    } finally { dispose(); element.remove(); }
  });
});

it("uses the transcript origin for row positions without duplicating outer chrome space", async () => {
  const element = mockScrollEl(300);
  const [origin, setOrigin] = createSignal(24);
  const shifts: number[] = [];
  let dispose!: () => void;
  const virtualizer = createRoot((stop) => {
    dispose = stop;
    return createTestTranscriptVirtualizer({
      items: () => Array.from({ length: 20 }, (_, index) => item("user", `origin-${index}`)),
      scrollElement: () => element,
      ...jsdomObservers(300, 400, 130),
      rowSizeHint: () => 100,
      originPx: origin,
      shiftContent: (delta) => { shifts.push(delta); return delta; },
    });
  });
  try {
    expect(transcriptReadingPosition(virtualizer)).toEqual({ rowKey: "origin-1", rowOffsetPx: 6 });
    expect(transcriptOffsetForPosition(virtualizer, { rowKey: "origin-1", rowOffsetPx: 10 })).toBe(134);
    const rows = [...virtualizer.getVirtualItems()];
    expect(transcriptVirtualRunway(rows, virtualizer.getTotalSize(), origin()).beforePx).toBe(rows[0]!.start - 24);
    setOrigin(80);
    await Promise.resolve();
    expect(shifts).toEqual([]);
    expect(virtualizer.scrollOffset).toBe(130);
    expect(virtualizer.measurementsCache[0]?.start).toBe(80);
  } finally { dispose(); }
});


it("delivers the latest application correction instead of an older captured scroll event", () => {
  const element = mockScrollEl(300);
  const motion = bindScrollportMotion(element, element, element);
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 40 }, (_, index) => item("user", `capture-${index}`)),
        scrollElement: () => element,
        observeElementRect: jsdomObservers(300).observeElementRect,
      });
      element.scrollTop = 1000;
      element.dispatchEvent(new Event("scroll"));
      scheduleScrollportFrame(element, "measure", () => motion.commit(1100, "content_shift"));
      flushScrollportFrameForTests(element);
      expect(virtualizer.scrollOffset).toBe(1100);
    } finally { dispose(); unbindScrollportMotion(element); element.remove(); }
  });
});


it("measures around a restored position before its deferred scroll delivery", () => {
  const element = mockScrollEl(300);
  const motion = bindScrollportMotion(element, element, element);
  createRoot((dispose) => {
    try {
      const virtualizer = createTestTranscriptVirtualizer({
        items: () => Array.from({ length: 40 }, (_, index) => item("user", `restore-${index}`)),
        scrollElement: () => element,
        ...jsdomObservers(300, 400, 1000),
        rowSizeHint: () => 100,
        shiftContent: (delta, fromOffset) => motion.shiftContent(delta, fromOffset),
      });
      motion.commit(250, "restore_anchor");
      // The reader sits in row 2; its growth is below the reading point.
      virtualizer.resizeItems([{ index: 2, size: 200 }]);
      expect(element.scrollTop).toBe(250);
      expect(transcriptReadingPosition(virtualizer)).toEqual({ rowKey: "restore-2", rowOffsetPx: 50 });
    } finally { dispose(); unbindScrollportMotion(element); element.remove(); }
  });
});


it("carries the captured row through successive layouts before native scroll delivery", async () => {
  const element = mockScrollEl(300);
  const motion = bindScrollportMotion(element, element, element);
  const [gap, setGap] = createSignal(14);
  let dispose!: () => void;
  const virtualizer = createRoot((stop) => {
    dispose = stop;
    return createTestTranscriptVirtualizer({
      items: () => Array.from({ length: 40 }, (_, index) => item("user", `captured-gap-${index}`)),
      scrollElement: () => element,
      ...jsdomObservers(300, 400, 1000),
      rowSizeHint: () => 100 + gap(),
      placementBasis: gap,
      shiftContent: (delta, fromOffset) => motion.shiftContent(delta, fromOffset),
    });
  });
  try {
    motion.commit(250, "restore_anchor");
    setGap(27.125);
    setGap(0);
    await Promise.resolve();
    expect(element.scrollTop).toBe(222);
    expect(transcriptReadingPosition(virtualizer)).toEqual({ rowKey: "captured-gap-2", rowOffsetPx: 22 });
  } finally { dispose(); unbindScrollportMotion(element); element.remove(); }
});


it("combines relayout and measurements against the offset before native contraction", async () => {
  const element = mockScrollEl(300);
  const motion = bindScrollportMotion(element, element, element);
  const [gap, setGap] = createSignal(14);
  let dispose!: () => void;
  const virtualizer = createRoot((stop) => {
    dispose = stop;
    return createTestTranscriptVirtualizer({
      items: () => Array.from({ length: 100 }, (_, index) => item("user", `clamp-${index}`)),
      scrollElement: () => element,
      ...jsdomObservers(300, 400, 10_500),
      rowSizeHint: () => 100 + gap(),
      placementBasis: gap,
      shiftContent: (delta, fromOffset) => motion.shiftContent(delta, fromOffset),
    });
  });
  try {
    motion.commit(10_500, "restore_anchor");
    setGap(0);
    // Contraction shrinks the extent and clamps the offset, before its scroll event is delivered.
    Object.defineProperty(element, "scrollHeight", { value: 10_000, configurable: true });
    element.scrollTop = 9_700;
    virtualizer.resizeItems([{ index: 0, size: 80 }]);
    await Promise.resolve();
    expect(element.scrollTop).toBe(9_192);
    expect(virtualizer.scrollOffset).toBe(9_192);
    expect(transcriptReadingPosition(virtualizer)).toEqual({ rowKey: "clamp-92", rowOffsetPx: 12 });
  } finally { dispose(); unbindScrollportMotion(element); element.remove(); }
});

it("returns null reading position when scroll element has clientHeight <= 0", () => {
  const element = document.createElement("div");
  Object.defineProperty(element, "clientHeight", { value: 0, configurable: true });
  const virtualizer = {
    scrollElement: element,
    scrollOffset: 0,
    options: { count: 10, getItemKey: (i: number) => `r-${i}` },
  } as unknown as TranscriptVirtualizer;
  expect(transcriptReadingPosition(virtualizer)).toBeNull();
});

