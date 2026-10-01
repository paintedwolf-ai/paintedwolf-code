import { createTranscriptRowMeasurements, type RowSize } from "./transcript-row-measurements.ts";
import {
  createVirtualizer,
  type Range,
  type VirtualItem,
  type Virtualizer,
  type VirtualizerOptions,
} from "@tanstack/solid-virtual";
import { batch, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import { observeSharedContentBox } from "../../../layout/shared-resize-observer.ts";
import { bufferedVirtualRange } from "../../../layout/virtual-runway.ts";
import { scrollportMotionForViewport } from "../../../platform/scrolling/scrollport-motion.ts";
import { observeScrollportOffset } from "../../../platform/scrolling/scrollport-offset.ts";
import { cancelScrollportFrame, scheduleScrollportFrame } from "../../../platform/scrolling/scrollport-frame.ts";
import {
  denScrollDebugLog,
  isStreamScrollDebugEnabled,
} from "../../stream/den-scroll-debug.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";

const ROW_ESTIMATE_MISS_REPORT_PX = 24;
const VIRTUAL_SHIFT_REPORT_PX = 24;
const ROW_RESIZE_REPORT_PX = 24;
const ROW_PART_CHANGE_PX = 4;
const ROW_PART_DEPTH = 3;
const ROW_PART_MEMORY = 400;

/** Part heights retained across row remounts. */
const rowPartHeightsByKey = new Map<string, Map<string, number>>();

/** Heights of a row's structural parts, keyed by position and first class. */
function rowPartSizes(row: Element): Map<string, number> {
  const sizes = new Map<string, number>();
  const visit = (el: Element, path: string, depth: number) => {
    Array.from(el.children).forEach((child, index) => {
      if (!(child instanceof HTMLElement)) return;
      const name = child.classList[0] ?? child.tagName.toLowerCase();
      const part = `${path}/${index}:${name}`;
      sizes.set(part, child.offsetHeight);
      if (depth < ROW_PART_DEPTH) visit(child, part, depth + 1);
    });
  };
  visit(row, "", 1);
  return sizes;
}

/** Logs a row whose measured height changed, naming the parts that changed. */
function reportRowResize(
  element: Element,
  key: VirtualItem["key"],
  previousSize: number | undefined,
  size: number,
): void {
  const rowKey = String(key);
  const parts = rowPartSizes(element);
  const previous = rowPartHeightsByKey.get(rowKey);
  rowPartHeightsByKey.delete(rowKey);
  rowPartHeightsByKey.set(rowKey, parts);
  if (rowPartHeightsByKey.size > ROW_PART_MEMORY) {
    const oldest = rowPartHeightsByKey.keys().next().value;
    if (oldest !== undefined) rowPartHeightsByKey.delete(oldest);
  }
  if (
    previousSize === undefined ||
    Math.abs(size - previousSize) < ROW_RESIZE_REPORT_PX
  ) {
    return;
  }
  const changed: string[] = [];
  for (const [part, height] of parts) {
    const before = previous?.get(part);
    if (before === undefined) changed.push(`${part} +${height}`);
    else if (Math.abs(height - before) >= ROW_PART_CHANGE_PX) {
      changed.push(`${part} ${before}→${height}`);
    }
  }
  for (const [part, height] of previous ?? []) {
    if (!parts.has(part)) changed.push(`${part} -${height}`);
  }
  denScrollDebugLog("scroll", "row-resize", {
    row: rowKey,
    from: Math.round(previousSize),
    to: size,
    parts: changed.slice(0, 8).join("; "),
  });
}

export const TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS = 6;

export const TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS = 1.5;

const TRANSCRIPT_VIRTUAL_INITIAL_VIEWPORT_HEIGHT_PX = 900;

/** Seed unmeasured rows by kind. */
export function transcriptRowEstimatedHeight(
  item: Pick<TranscriptItem, "kind">,
): number {
  switch (item.kind) {
    case "worker_file_edit":
    case "file_edit":
      return 120;
    case "activity_span":
      return 64;
    case "worker_group":
      return 136;
    case "blueprint_card":
      return 160;
    case "workflow_feedback":
    case "workflow_explain":
    case "checkpoint":
      return 40;
    case "progress_complete":
    case "progress_update":
      return 56;
    case "workflow_boundary":
      return 32;
    case "time_marker":
      return 24;
    case "unread_marker":
      return 16;
    case "turn_tail":
      return 20;
    case "pending_user":
    case "user":
    case "assistant":
    case "draft":
    case "tool":
    case "turn_load":
    case "index_warming":
    case "fallback":
      return 72;
    default: {
      const _exhaustive: never = item.kind;
      return _exhaustive;
    }
  }
}

export type TranscriptVirtualizer = Virtualizer<HTMLElement, Element>;
export type MeasuredTranscriptVirtualizer = TranscriptVirtualizer & {
  queueMeasurement: (element: Element) => void;
  forgetMeasurement: (element: Element) => void;
  remeasureMounted: () => void;
  resizeItems: (sizes: readonly RowSize[]) => void;
};

export type TranscriptVirtualRunway = {
  beforePx: number;
  afterPx: number;
};

function nonNegativeFinite(value: number): number {
  return Number.isFinite(value) ? Math.max(0, value) : 0;
}

/** Unmounted space keeps the end sentinel at the full transcript height. */
export function transcriptVirtualRunway(
  rows: readonly Pick<VirtualItem, "start" | "end">[],
  totalSize: number,
  originPx = 0,
): TranscriptVirtualRunway {
  const total = nonNegativeFinite(totalSize);
  const first = rows[0];
  const last = rows[rows.length - 1];
  if (!first || !last) return { beforePx: 0, afterPx: total };
  return {
    beforePx: nonNegativeFinite(first.start - originPx),
    afterPx: nonNegativeFinite(total - nonNegativeFinite(last.end - originPx)),
  };
}

/** Initial tail offset in the shared scrollport. */
export function transcriptInitialEndOffset(
  scrollElement: HTMLElement | null,
  localEndOffset: number,
): number {
  const local = Number.isFinite(localEndOffset)
    ? Math.max(0, localEndOffset)
    : 0;
  if (!scrollElement?.closest(".den-chat-stream")) return local;
  const shared = scrollElement.scrollHeight - scrollElement.clientHeight;
  return Number.isFinite(shared) ? Math.max(local, shared) : local;
}

export function transcriptViewportRect(
  observed: { width: number; height: number; borderWidth?: number; borderHeight?: number },
): { width: number; height: number } {
  return {
    // The transcript host has no border or native scrollbar gutter.
    width: Math.round(observed.borderWidth ?? observed.width),
    height: Math.round(observed.borderHeight ?? observed.height),
  };
}

/** Observe the settled viewport box. */
export function observeTranscriptViewportRect(
  instance: TranscriptVirtualizer,
  cb: (rect: { width: number; height: number }) => void,
): () => void {
  const element = instance.scrollElement;
  if (!element) return () => {};
  return observeSharedContentBox(element, (box) => {
    cb(transcriptViewportRect(box));
  });
}

/** The offset counts as settled this long after the last scroll event. */
const SCROLL_SETTLE_MS = 150;

/** Capture input before rendering; virtual rows change at most once per frame. */
export function observeTranscriptScrollOffset(
  instance: TranscriptVirtualizer,
  cb: (offset: number, isScrolling: boolean) => void,
): () => void {
  const element = instance.scrollElement;
  if (!element) return () => {};
  let settle: ReturnType<typeof setTimeout> | undefined;
  let offset = element.scrollTop;
  const currentOffset = () => scrollportMotionForViewport(element)?.offsetY() ?? offset;
  const deliver = () => {
    cb(currentOffset(), true);
  };
  const onScroll = (observed: number) => {
    offset = observed;
    scheduleScrollportFrame(element, "render", deliver);
    clearTimeout(settle);
    settle = setTimeout(() => {
      settle = undefined;
      cancelScrollportFrame(element, deliver);
      cb(currentOffset(), false);
    }, SCROLL_SETTLE_MS);
  };
  const stop = observeScrollportOffset(element, onScroll);
  cb(offset, false);
  return () => {
    clearTimeout(settle);
    cancelScrollportFrame(element, deliver);
    stop();
  };
}

/** Read a painted row size without replacing a useful estimate with zero. */
function measureTranscriptRow(
  element: Element,
  entry: ResizeObserverEntry | undefined,
  instance: Virtualizer<HTMLElement, Element>,
): { size: number; width?: number; observed: boolean } {
  const box = entry?.borderBoxSize?.[0];
  const rect = box ? undefined : element.getBoundingClientRect();
  const measured = box
    ? box.blockSize
    : rect?.height || (element as HTMLElement).offsetHeight;
  if (measured > 0) return { size: measured, width: box?.inlineSize ?? rect?.width, observed: true };
  const index = instance.indexFromElement(element);
  if (index < 0) return { size: 0, observed: false };
  const cached = instance.itemSizeCache.get(instance.options.getItemKey(index));
  return {
    size: cached ?? instance.options.estimateSize(index),
    observed: false,
  };
}

/** The row under the scrollport top and how far the top sits into it. */
export type TranscriptReadingPosition = { rowKey: string; rowOffsetPx: number };

type ReadingAnchor = { index: number; key: VirtualItem["key"]; start: number };

/** Row under the reading offset in the current layout. */
function readingAnchor(instance: TranscriptVirtualizer, offset = instance.scrollOffset): ReadingAnchor | null {
  if (offset === null || !instance.scrollElement) return null;
  if (instance.scrollElement.clientHeight <= 0) return null;
  if (instance.options.count === 0) return null;
  let row = instance.getVirtualItemForOffset(offset);
  if (row && row.end <= offset) row = instance.measurementsCache[row.index + 1] ?? row;
  return row ? { index: row.index, key: row.key, start: row.start } : null;
}

/** How far the anchored row moved between two layouts. */
function readingAnchorDrift(
  instance: TranscriptVirtualizer,
  anchor: ReadingAnchor,
  originDelta = 0,
): number {
  const { count, getItemKey } = instance.options;
  let index =
    anchor.index < count && getItemKey(anchor.index) === anchor.key
      ? anchor.index
      : -1;
  for (let candidate = 0; index < 0 && candidate < count; candidate += 1) {
    if (getItemKey(candidate) === anchor.key) index = candidate;
  }
  if (index < 0) return 0;
  // Rebuild measurements for the anchored offset.
  instance.getVirtualItemForOffset(anchor.start);
  const row = instance.measurementsCache[index];
  return row ? row.start - anchor.start - originDelta : 0;
}

/** Skipped chrome anchors to the next accepted row with a negative offset. */
export function transcriptReadingPosition(
  instance: TranscriptVirtualizer,
  anchorable: (rowKey: string) => boolean = () => true,
  offset = instance.scrollOffset,
): TranscriptReadingPosition | null {
  if (instance.scrollElement && instance.scrollElement.clientHeight <= 0) return null;
  const anchor = readingAnchor(instance, offset);
  if (!anchor || offset === null) return null;
  const { count, getItemKey } = instance.options;
  for (let index = anchor.index; index < count; index += 1) {
    const rowKey = String(getItemKey(index));
    if (!anchorable(rowKey)) continue;
    const start = index === anchor.index ? anchor.start : instance.measurementsCache[index]?.start;
    if (start === undefined) return null;
    return { rowKey, rowOffsetPx: offset - start };
  }
  return null;
}

/** Offset that shows a reading position again; null while its row is absent. */
export function transcriptOffsetForPosition(
  instance: TranscriptVirtualizer,
  position: TranscriptReadingPosition,
): number | null {
  const { count, getItemKey } = instance.options;
  for (let index = 0; index < count; index += 1) {
    if (String(getItemKey(index)) !== position.rowKey) continue;
    // Any offset lookup rebuilds measurements for the current options.
    instance.getVirtualItemForOffset(0);
    const row = instance.measurementsCache[index];
    return row ? Math.max(0, row.start + position.rowOffsetPx) : null;
  }
  return null;
}

type TranscriptRowMeasurement = {
  element: Element;
  index: number;
  size: number;
  width?: number;
};

export type CreateTranscriptVirtualizerOptions<
  T extends TranscriptItem = TranscriptItem,
> = {
  items: () => readonly T[];
  scrollElement: () => HTMLElement | null;
  /** Moves to an index or offset scroll's target; a smooth request glides there. */
  revealOffset: (offset: number, glide: boolean) => void;
  /** Carries the offset with content that moved above it; returns the applied shift. */
  shiftContent: (deltaY: number, fromOffset: number) => number;
  rowSizeHint?: (item: T) => number | undefined;
  /** Inputs to `rowSizeHint` beyond the item; unmeasured rows re-estimate when they change. */
  estimateBasis?: () => unknown;
  /** Placement changes refresh unmeasured estimates without discarding measured interiors. */
  placementBasis?: () => unknown;
  originPx?: () => number;
  minimumBufferRows?: number;
  bufferViewports?: number;
  observeElementRect?: VirtualizerOptions<
    HTMLElement,
    Element
  >["observeElementRect"];
  observeElementOffset?: VirtualizerOptions<
    HTMLElement,
    Element
  >["observeElementOffset"];
  onRowMeasured?: (measurement: TranscriptRowMeasurement) => void;
  onChange?: VirtualizerOptions<HTMLElement, Element>["onChange"];
};

export function createTranscriptVirtualizer<T extends TranscriptItem>(
  opts: CreateTranscriptVirtualizerOptions<T>,
): MeasuredTranscriptVirtualizer {
  let measurementDepth = 0;
  let measurementShift = 0;
  let pendingRowDrift = 0;
  let measurementAnchorIndex: number | undefined;
  const minimumBufferRows = Math.max(
    0,
    Math.floor(opts.minimumBufferRows ?? TRANSCRIPT_VIRTUAL_MIN_BUFFER_ROWS),
  );
  const bufferViewports = Math.max(
    0,
    opts.bufferViewports ?? TRANSCRIPT_VIRTUAL_BUFFER_VIEWPORTS,
  );
  const estimatedSize = (index: number): number => {
    const item = opts.items()[index];
    if (!item) return 72;
    return opts.rowSizeHint?.(item) ?? transcriptRowEstimatedHeight(item);
  };
  const initialEndOffset = (): number => {
    const count = opts.items().length;
    if (count === 0) return 0;
    // Seams live inside each row's box, so the extent is the sum of row sizes.
    let total = 0;
    for (let index = 0; index < count; index += 1) {
      total += estimatedSize(index);
    }
    const viewportHeight =
      opts.scrollElement()?.clientHeight ||
      TRANSCRIPT_VIRTUAL_INITIAL_VIEWPORT_HEIGHT_PX;
    return transcriptInitialEndOffset(
      opts.scrollElement(),
      total - viewportHeight,
    );
  };
  const [measurementEpoch, setMeasurementEpoch] = createSignal(0);
  let disposed = false;
  onCleanup(() => { disposed = true; });
  let measurementRefreshQueued = false;
  const scheduleMeasuredRangeRefresh = (): void => {
    if (measurementRefreshQueued) return;
    measurementRefreshQueued = true;
    queueMicrotask(() => {
      measurementRefreshQueued = false;
      if (disposed) return;
      setMeasurementEpoch((epoch) => epoch + 1);
    });
  };
  const measuredRows: TranscriptRowMeasurement[] = [];
  const measureAndRefreshRange = (
    element: Element,
    entry: ResizeObserverEntry | undefined,
    instance: Virtualizer<HTMLElement, Element>,
    collect = false,
  ): number => {
    const measurement = measureTranscriptRow(element, entry, instance);
    const size = measurement.size;
    const index = instance.indexFromElement(element);
    if (index < 0 || size <= 0) return size;
    const key = instance.options.getItemKey(index);
    const cached = instance.itemSizeCache.get(key);
    const prior = cached ?? instance.options.estimateSize(index);
    if (prior !== size) scheduleMeasuredRangeRefresh();
    // A first measurement far from its estimate moves the scroll range.
    if (cached === undefined && Math.abs(size - prior) >= ROW_ESTIMATE_MISS_REPORT_PX) {
      denScrollDebugLog("scroll", "row-estimate-miss", {
        row: String(key),
        estimated: Math.round(prior),
        measured: size,
      });
    }
    if (measurement.observed) {
      if (collect && opts.onRowMeasured) measuredRows.push({ element, index, size, width: measurement.width });
      if (
        isStreamScrollDebugEnabled() &&
        !element.querySelector('[data-animating="true"]')
      ) {
        reportRowResize(element, key, cached, size);
      }
    }
    return size;
  };
  const virtualizerRef: { current?: TranscriptVirtualizer } = {};
  const viewportBufferPx = (): number => {
    const viewportHeight =
      virtualizerRef.current?.scrollRect?.height ||
      opts.scrollElement()?.clientHeight ||
      TRANSCRIPT_VIRTUAL_INITIAL_VIEWPORT_HEIGHT_PX;
    return Math.max(0, viewportHeight * bufferViewports);
  };
  const bufferedSize = (index: number): number => {
    const item = opts.items()[index];
    const cached = virtualizerRef.current?.itemSizeCache.get(item?.key ?? index);
    return cached ?? estimatedSize(index);
  };
  const rangeExtractor = createMemo(() => {
    measurementEpoch();
    return (range: Range) =>
      bufferedVirtualRange({
        range,
        bufferPx: viewportBufferPx(),
        rowSize: bufferedSize,
      });
  });
  // Key changes invalidate memoized row measurements.
  const itemKeys = createMemo(
    () => opts.items().map((item) => item.key),
    [] as (string | number)[],
    {
      equals: (a, b) => a.length === b.length && a.every((key, i) => key === b[i]),
    },
  );
  const getItemKey = createMemo(() => {
    // The core rebuilds unmeasured estimates only for a new key function.
    opts.estimateBasis?.();
    opts.placementBasis?.();
    // Each layout resolves the keys it was built from.
    const keys = itemKeys();
    return (index: number) => keys[index] ?? index;
  });
  const virtualizer = createVirtualizer({
    get count() {
      return opts.items().length;
    },
    getScrollElement: () => opts.scrollElement(),
    estimateSize: estimatedSize,
    get getItemKey() {
      return getItemKey();
    },
    overscan: minimumBufferRows,
    get rangeExtractor() {
      return rangeExtractor();
    },
    get scrollMargin() { return opts.originPx?.() ?? 0; },
    measureElement: measureAndRefreshRange,
    // Each adjustment is relative to the current virtual offset.
    scrollToFn: (offset, { adjustments, behavior }, instance) => {
      if (adjustments === undefined) {
        opts.revealOffset(offset, behavior === "smooth");
        return;
      }
      if (measurementDepth > 0) {
        measurementShift += adjustments;
        return;
      }
      const applied = opts.shiftContent(adjustments, offset);
      // The core adds the requested correction next; keep only what applied.
      if (instance.scrollOffset !== null) {
        instance.scrollOffset -= adjustments - applied;
      }
    },
    initialRect: {
      width: 800,
      height: TRANSCRIPT_VIRTUAL_INITIAL_VIEWPORT_HEIGHT_PX,
    },
    initialOffset: initialEndOffset,
    // The outer motion controller anchors against the shared scrollport tail.
    anchorTo: "start",
    followOnAppend: false,
    observeElementRect: opts.observeElementRect ?? observeTranscriptViewportRect,
    observeElementOffset:
      opts.observeElementOffset ?? observeTranscriptScrollOffset,
    onChange: opts.onChange,
  }) as MeasuredTranscriptVirtualizer;
  const synchronizeScrollOffset = () => {
    const viewport = opts.scrollElement();
    const captured = viewport && scrollportMotionForViewport(viewport)?.offsetY();
    if (captured == null || virtualizer.scrollOffset === null) return;
    // Layout may run before deferred rendering receives a restore or native scroll.
    virtualizer.scrollOffset = Math.max(0, captured + pendingRowDrift + measurementShift);
  };
  virtualizer.resizeItems = (sizes) => {
    if (sizes.length === 0) return;
    if (measurementDepth === 0) {
      synchronizeScrollOffset();
      const offset = virtualizer.scrollOffset ?? 0;
      const anchor = virtualizer.getVirtualItemForOffset(offset);
      measurementAnchorIndex = anchor ? anchor.index + (anchor.end <= offset ? 1 : 0) : undefined;
    }
    measurementDepth++;
    try {
      batch(() => {
        for (const { index, size } of sizes) virtualizer.resizeItem(index, size);
      });
    } finally {
      measurementDepth--;
      if (measurementDepth === 0) measurementAnchorIndex = undefined;
      if (measurementDepth === 0 && measurementShift !== 0) {
        const requested = measurementShift + pendingRowDrift;
        measurementShift = 0;
        pendingRowDrift = 0;
        // Published virtual extent makes the corrected native offset reachable.
        const applied = opts.shiftContent(requested, (virtualizer.scrollOffset ?? requested) - requested);
        if (virtualizer.scrollOffset !== null && applied !== requested) {
          virtualizer.scrollOffset = Math.max(0, virtualizer.scrollOffset + applied - requested);
        }
      }
    }
  };
  const measurements = createTranscriptRowMeasurements({
    viewport: opts.scrollElement,
    generation: () => opts.estimateBasis?.(),
    identity: (element) => {
      if (!element.hasAttribute(virtualizer.options.indexAttribute)) return null;
      const index = virtualizer.indexFromElement(element);
      if (index < 0 || index >= virtualizer.options.count) return null;
      const key = String(virtualizer.options.getItemKey(index));
      const renderedKey = element.getAttribute("data-msg-id") ?? element.getAttribute("data-time-row");
      return renderedKey !== null && renderedKey !== key ? null : { key, index };
    },
    read: (element, entry) => measureAndRefreshRange(element, entry, virtualizer, true),
    publish: (sizes) => {
      const observations = measuredRows.splice(0);
      virtualizer.resizeItems(sizes);
      // Height hints publish after the batch consumes its prior estimates.
      for (const row of observations) opts.onRowMeasured?.(row);
    },
  });
  virtualizer.queueMeasurement = measurements.queue;
  virtualizer.forgetMeasurement = measurements.forget;
  virtualizer.remeasureMounted = measurements.remeasure;
  virtualizer.measureElement = (element) => {
    if (element) measurements.measure([element]);
    else measurements.sweep();
  };
  onCleanup(measurements.dispose);
  // Only size changes wholly above the reading offset move the reader.
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (
    item,
    delta,
    instance,
  ) => {
    const scrollOffset =
      instance.scrollOffset ?? instance.scrollElement?.scrollTop ?? 0;
    const adjust = measurementAnchorIndex === undefined
      ? item.end <= scrollOffset
      : item.index < measurementAnchorIndex;
    if (adjust && Math.abs(delta) >= VIRTUAL_SHIFT_REPORT_PX) {
      denScrollDebugLog("scroll", "virtual-shift", {
        cause: instance.itemSizeCache.has(item.key) ? "re-measure" : "first-measure",
        row: String(item.key),
        start: Math.round(item.start),
        end: Math.round(item.end),
        delta: Math.round(delta),
        offset: Math.round(scrollOffset),
      });
    }
    return adjust;
  };
  virtualizerRef.current = virtualizer;
  // Carry the reading offset immediately; shift the DOM after rows mount.
  const carryRowDrift = () => {
    const drift = pendingRowDrift;
    pendingRowDrift = 0;
    if (disposed || drift === 0) return;
    const applied = opts.shiftContent(drift, (virtualizer.scrollOffset ?? drift) - drift);
    if (applied !== drift && virtualizer.scrollOffset !== null) {
      virtualizer.scrollOffset = Math.max(0, virtualizer.scrollOffset + applied - drift);
    }
  };
  /**
   * A geometry change re-estimates only the rows the DOM does not show. Mounted rows keep
   * their measurement until the remeasure lands, so the reader moves once, by the real delta,
   * instead of by the estimate error and back.
   */
  const reestimateUnmountedRows = () => {
    const mounted = measurements.mountedKeys();
    const { count, getItemKey } = virtualizer.options;
    const sizes: RowSize[] = [];
    for (let index = 0; index < count; index += 1) {
      const key = getItemKey(index);
      if (mounted.has(String(key))) continue;
      const estimate = estimatedSize(index);
      if (virtualizer.itemSizeCache.get(key) !== estimate) sizes.push({ index, size: estimate });
    }
    virtualizer.resizeItems(sizes);
  };
  const carryRowChanges = (anchor: ReadingAnchor | null, countBefore: number, originBefore: number) => {
    if (!anchor) return;
    // Carry changes within the transcript; outer chrome moves the origin separately.
    const drift = readingAnchorDrift(virtualizer, anchor, virtualizer.options.scrollMargin - originBefore);
    if (drift === 0 || virtualizer.scrollOffset === null) return;
    if (Math.abs(drift) >= VIRTUAL_SHIFT_REPORT_PX) {
      denScrollDebugLog("scroll", "virtual-shift", {
        cause: "rows",
        row: String(anchor.key),
        start: Math.round(anchor.start),
        delta: Math.round(drift),
        offset: Math.round(virtualizer.scrollOffset),
        countBefore,
        countAfter: virtualizer.options.count,
      });
    }
    virtualizer.scrollOffset = Math.max(0, virtualizer.scrollOffset + drift);
    if (pendingRowDrift === 0) queueMicrotask(carryRowDrift);
    pendingRowDrift += drift;
  };
  const setOptions = virtualizer.setOptions;
  let measuredBasis = untrack(() => opts.estimateBasis?.());
  virtualizer.setOptions = (next) => {
    synchronizeScrollOffset();
    const anchor = readingAnchor(virtualizer);
    const countBefore = virtualizer.options.count;
    const originBefore = virtualizer.options.scrollMargin;
    const basis = untrack(() => opts.estimateBasis?.());
    const geometryChanged = basis !== measuredBasis;
    measuredBasis = basis;
    setOptions(next);
    carryRowChanges(anchor, countBefore, originBefore);
    if (geometryChanged) reestimateUnmountedRows();
  };
  return virtualizer;
}
