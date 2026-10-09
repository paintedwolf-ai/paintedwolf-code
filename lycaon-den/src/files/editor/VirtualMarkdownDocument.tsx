import { createVirtualizer, type VirtualItem, type Virtualizer } from "@tanstack/solid-virtual";
import { For, batch, createEffect, createMemo, createSignal, onCleanup, onMount, untrack } from "solid-js";
import type { Token } from "marked";
import type { MarkdownPreviewBlock } from "../../chat/markdown/markdown-preview-document.ts";
import { bufferedVirtualRange } from "../../layout/virtual-runway.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { afterPaint } from "../../ui/surface-reveal.ts";
import { MarkdownBody } from "../../components/transcript/MarkdownBody.tsx";
import { createMarkdownBlockSizer } from "./markdown-block-sizer.ts";

export type PreviewDocument = {
  blocks: MarkdownPreviewBlock[];
  ready: () => void;
  read: (index: number, receive: (tokens: Token[]) => void) => () => void;
};

type PaintedBlock = VirtualItem & { tokens: Token[] };
const CACHE_BLOCKS = 128;
/** Waits out a scroll gesture before the extent changes again. */
const SIZE_SWEEP_RETRY_MS = 200;
/** Settles a drag-resize before every block is sized again. */
const SIZE_SWEEP_WIDTH_MS = 150;
/** Tokenization retries before retaining the estimated height. */
const SIZE_SWEEP_MAX_WAITS = 25;

export function VirtualMarkdownDocument(props: {
  document: PreviewDocument;
  projectId: string;
  scrollport: () => HTMLDivElement;
}) {
  const cache = new Map<number, Token[]>();
  const requests = new Map<number, () => void>();
  /** Blocks the sizer could not measure; their estimate stands. */
  const unsizable = new Set<number>();
  const [revision, setRevision] = createSignal(0);
  const [offset, setOffset] = createSignal(0);
  const [painted, setPainted] = createSignal<PaintedBlock[]>([]);
  const [paintedOffset, setPaintedOffset] = createSignal(0);
  let disposed = false;
  let published = false;
  let windowEl: HTMLDivElement | undefined;
  const sizer = createMarkdownBlockSizer(() => windowEl, () => props.projectId);
  const motion = () => scrollportMotionForViewport(props.scrollport());
  const estimateSize = (index: number) => {
    const block = props.document.blocks[index];
    return block ? Math.max(48, Math.max(block.lines, block.chars / 85) * 22) : 48;
  };
  const initialized: { current?: Virtualizer<HTMLDivElement, HTMLDivElement> } = {};
  const virtualizer: Virtualizer<HTMLDivElement, HTMLDivElement> = createVirtualizer({
    count: props.document.blocks.length,
    getScrollElement: props.scrollport,
    estimateSize,
    overscan: 2,
    gap: 14,
    rangeExtractor: (range) => bufferedVirtualRange({
      range,
      bufferPx: props.scrollport().clientHeight * 1.5,
      rowSize: (index) => initialized.current?.itemSizeCache.get(index) ?? estimateSize(index),
      gapPx: 14,
    }),
    observeElementOffset: (instance, receive) => {
      const element = instance.scrollElement;
      if (!element) return () => {};
      const sample = () => batch(() => {
        setOffset(element.scrollTop);
        receive(element.scrollTop, false);
      });
      element.addEventListener("scroll", sample, { passive: true });
      // Application offsets update the visible range before paint.
      const stop = motion()?.subscribeCommits(sample);
      sample();
      return () => {
        element.removeEventListener("scroll", sample);
        stop?.();
      };
    },
    scrollToFn: (next, { adjustments, behavior }) => {
      const controller = motion();
      // Height changes above the reader shift the reading position.
      if (controller && adjustments !== undefined) controller.commitShift(adjustments, next, "layout_compensation");
      else if (controller && behavior === "smooth") void controller.revealOffset(next);
      else if (controller) controller.commit(next, "layout_compensation");
      else props.scrollport().scrollTo({ top: next + (adjustments ?? 0) });
    },
  });
  initialized.current = virtualizer;
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) =>
    !motion()?.input.isDirectInputActive() && item.end <= (instance.scrollOffset ?? 0);

  const wanted = createMemo(() => virtualizer.getVirtualItems().map((item) => ({ ...item })));
  const ready = createMemo(() => {
    revision();
    if (props.document.blocks.length === 0) return true;
    const top = offset();
    const bottom = top + (virtualizer.scrollRect?.height ?? props.scrollport().clientHeight);
    const visible = wanted().filter((item) => item.end > top && item.start < bottom);
    // Only visible cache misses retain the painted window.
    return visible.length > 0 && visible.every((item) => cache.has(item.index));
  });

  let sweepFor: number | undefined;
  let sweepWaits = 0;
  let sweep: { cancel: () => void; delay: number } | undefined;
  /** Set once the box reports no height at all — nothing here can be sized. */
  let sizingUnavailable = false;

  const evictCached = () => {
    const protectedItems = new Set(untrack(wanted).map((entry) => entry.index));
    for (const index of cache.keys()) {
      if (cache.size <= CACHE_BLOCKS) break;
      if (!protectedItems.has(index)) cache.delete(index);
    }
  };

  const sized = (index: number) =>
    virtualizer.itemSizeCache.has(index) || unsizable.has(index);

  /** Gives a block its rendered height before it is ever painted. */
  function sizeBlock(index: number, tokens: Token[]): void {
    if (sized(index) || sizingUnavailable) return;
    let height: number | null = null;
    try {
      height = sizer.measure(tokens);
    } catch {
      height = null;
    }
    if (height === null) {
      // No height means the box has no layout. Sizing stops until the width changes.
      sizingUnavailable = true;
      sweepFor = undefined;
      sizer.release();
      return;
    }
    virtualizer.resizeItem(index, height);
    // Rejected measurements retain estimates and advance the sweep.
    if (!virtualizer.itemSizeCache.has(index)) unsizable.add(index);
  }

  function scheduleSizeSweep(delay: number): void {
    if (disposed || (sweep && sweep.delay <= delay)) return;
    sweep?.cancel();
    const timer = setTimeout(() => {
      sweep = undefined;
      runSizeSweep();
    }, delay);
    sweep = { cancel: () => clearTimeout(timer), delay };
  }

  /** Sizes one unmeasured block per turn, never during a scroll gesture. */
  function runSizeSweep(): void {
    if (disposed || sizingUnavailable) return;
    if (motion()?.input.isDirectInputActive()) {
      scheduleSizeSweep(SIZE_SWEEP_RETRY_MS);
      return;
    }
    let next: number | undefined;
    for (let index = 0; index < props.document.blocks.length; index += 1) {
      if (!sized(index)) {
        next = index;
        break;
      }
    }
    if (next === undefined) {
      sweepFor = undefined;
      sizer.release();
      return;
    }
    const cached = cache.get(next);
    if (cached) {
      sweepFor = undefined;
      sizeBlock(next, cached);
      scheduleSizeSweep(0);
      return;
    }
    // The paint path shares this request; its arrival resumes the sweep.
    sweepWaits = next === sweepFor ? sweepWaits + 1 : 0;
    if (sweepWaits > SIZE_SWEEP_MAX_WAITS) {
      unsizable.add(next);
      sweepFor = undefined;
      scheduleSizeSweep(0);
      return;
    }
    sweepFor = next;
    request(next);
    scheduleSizeSweep(SIZE_SWEEP_RETRY_MS);
  }

  function request(index: number): void {
    if (requests.has(index)) return;
    requests.set(index, () => {});
    const cancel = props.document.read(index, (tokens) => {
      if (disposed) return;
      requests.get(index)?.();
      requests.delete(index);
      cache.set(index, tokens);
      sizeBlock(index, tokens);
      evictCached();
      setRevision((value) => value + 1);
      scheduleSizeSweep(0);
    });
    if (requests.has(index)) requests.set(index, cancel);
    else cancel();
  }

  createEffect(() => {
    const desired = new Set(wanted().map((item) => item.index));
    for (const [index, cancel] of requests) {
      if (desired.has(index) || index === sweepFor) continue;
      cancel();
      requests.delete(index);
    }
    for (const item of wanted()) {
      const cached = cache.get(item.index);
      if (cached) {
        cache.delete(item.index);
        cache.set(item.index, cached);
      } else {
        request(item.index);
      }
    }
  });

  createEffect(() => {
    const currentOffset = offset();
    if (!ready()) return;
    const next: PaintedBlock[] = [];
    for (const item of wanted()) {
      const tokens = cache.get(item.index);
      if (!tokens) continue;
      next.push({ ...item, tokens });
    }
    batch(() => {
      setPainted(next);
      setPaintedOffset(currentOffset);
    });
  });

  createEffect(() => {
    if (published || !ready()) return;
    onCleanup(afterPaint(() => {
      if (!ready()) return;
      published = true;
      props.document.ready();
    }));
  });
  onCleanup(() => {
    disposed = true;
    sweep?.cancel();
    sweep = undefined;
    sizer.release();
    for (const cancel of requests.values()) cancel();
    requests.clear();
    cache.clear();
  });

  /** Measured heights belong to one column width. */
  const observeColumnWidth = (element: HTMLDivElement) => {
    if (typeof ResizeObserver !== "function") return;
    let width = element.clientWidth;
    let settle: ReturnType<typeof setTimeout> | undefined;
    const observer = new ResizeObserver(() => {
      if (Math.abs(element.clientWidth - width) < 0.5) return;
      width = element.clientWidth;
      if (settle !== undefined) clearTimeout(settle);
      settle = setTimeout(() => {
        settle = undefined;
        if (disposed) return;
        unsizable.clear();
        sizingUnavailable = false;
        virtualizer.measure();
        scheduleSizeSweep(0);
      }, SIZE_SWEEP_WIDTH_MS);
    });
    observer.observe(element);
    onCleanup(() => {
      if (settle !== undefined) clearTimeout(settle);
      observer.disconnect();
    });
  };

  const retained = () => !ready() && painted().length > 0;
  return (
    <div
      class="den-markdown-preview-window"
      ref={(element) => {
        windowEl = element;
        onMount(() => {
          observeColumnWidth(element);
          scheduleSizeSweep(0);
        });
      }}
      style={{ height: `${virtualizer.getTotalSize()}px`, overflow: "clip", "transition-property": "none" }}
    >
      <div
        class="den-retained-presentation"
        data-retained={retained() ? "true" : "false"}
        aria-busy={!ready()}
        inert={retained() ? true : undefined}
      >
        <For each={painted().map((item) => item.index)}>{(index) => {
          const initial = untrack(() => painted().find((entry) => entry.index === index));
          if (!initial) return null;
          const item = createMemo<PaintedBlock>((previous) => painted().find((entry) => entry.index === index) ?? previous ?? initial, initial);
          return (
            <div
              data-index={index}
              ref={(element) => onMount(() => virtualizer.measureElement(element))}
              class="den-markdown-preview-block"
              style={{
                transform: `translateY(${item().start + (retained() ? offset() - paintedOffset() : 0)}px)`,
                "transition-property": "none",
              }}
            >
              <MarkdownBody source={item().tokens} projectId={props.projectId} />
            </div>
          );
        }}</For>
      </div>
    </div>
  );
}
