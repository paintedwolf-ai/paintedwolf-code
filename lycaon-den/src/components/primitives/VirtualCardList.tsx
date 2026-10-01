import { observeTranscriptScrollOffset } from "../../chat/transcript/layout/transcript-virtualizer.ts";
import { Show, createComputed, createEffect, createMemo, createSignal, on, onCleanup, onMount, type JSX } from "solid-js";
import { createVirtualizer, defaultRangeExtractor, observeElementRect, type Range } from "@tanstack/solid-virtual";
import { KeyedIndex } from "../keyed-index.tsx";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { Scrollport } from "./Scrollport.tsx";
import { bindVirtualListFind, type VirtualListMatch } from "../../find/virtual-list-find.ts";
import { registerFindRevealHost } from "../../find/find-reveal.ts";
import { observeSharedContentBox } from "../../layout/shared-resize-observer.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../../chat/transcript/presentation/transcript-disclosure.tsx";

/** Where the reader was within the list: a row's top, or the list's end when below every row. */
type ReadingAnchor = { key: string; top: number } | { end: number };

function sameKeys(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((key, index) => key === right[index]);
}

/** Large lists scroll with an enclosing viewport or own a bounded scroll area. */
export function VirtualCardList<T>(props: {
  items: readonly T[];
  keyOf: (item: T) => string;
  children: (item: () => T) => JSX.Element;
  label: string;
  scroll?: "self" | "ancestor";
  /** Estimated height until a row is measured. */
  estimateSize?: number | ((item: T) => number);
  /** Reports each row measurement after layout. */
  onMeasure?: (item: T, element: HTMLElement) => void;
  threshold?: number;
  /** Rows kept mounted beyond the viewport; lower it when a row is expensive. */
  overscan?: number;
  /**
   * With `scroll="ancestor"`, rows added or removed above the reader keep what they are
   * reading in place. Each item renders one root element.
   */
  keepReadingPosition?: boolean;
  pinnedKeys?: readonly string[];
  /** Items with their own Find provider stay mounted. */
  externalFind?: (item: T) => boolean;
  onReveal?: (reveal: ((key: string) => void) | undefined) => void;
  corpus?: (item: T) => string;
  search?: (item: T, query: string, sensitive: boolean, signal: AbortSignal) => Promise<VirtualListMatch[]>;
  onFindReveal?: (item: T, match: VirtualListMatch) => void;
}) {
  const [host, setHost] = createSignal<HTMLDivElement>();
  const [flowHost, setFlowHost] = createSignal<HTMLDivElement>();
  const [focusedKey, setFocusedKey] = createSignal<string>();
  const [scrollHost, setScrollHost] = createSignal<HTMLElement>();
  const [origin, setOrigin] = createSignal(0);
  const scrollsAncestor = () => props.scroll === "ancestor";
  const measureOrigin = () => {
    const element = host(), scroller = scrollHost();
    if (!element || !scroller || !scrollsAncestor() || !element.getClientRects().length) return;
    const top = scroller === document.scrollingElement ? 0 : scroller.getBoundingClientRect().top + scroller.clientTop;
    const next = element.getBoundingClientRect().top - top + scroller.scrollTop;
    if (next !== origin()) setOrigin(next);
  };
  const large = () => props.items.length > (props.threshold ?? (scrollsAncestor() ? 60 : 12));
  const keepsReadingPosition = () => scrollsAncestor() && props.keepReadingPosition === true;
  const keys = createMemo(() => props.items.map(props.keyOf), undefined, { equals: sameKeys });
  const estimates = createMemo(() => {
    const estimate = props.estimateSize;
    return typeof estimate === "function" ? props.items.map(estimate) : undefined;
  });
  // A new key function refreshes estimates without discarding measured heights.
  const getItemKey = createMemo(() => {
    const current = keys();
    void estimates();
    return (index: number) => {
      const key = current[index];
      if (key === undefined) throw new Error("Virtual row index is outside the current list.");
      return key;
    };
  });
  const rangeExtractor = createMemo(() => {
    const pinned=new Set([...(props.pinnedKeys ?? []),focusedKey()]);
    const indices=props.items.flatMap((item,index)=>pinned.has(props.keyOf(item)) || props.externalFind?.(item) ? [index] : []);
    return (range: Range) => [...new Set([...defaultRangeExtractor(range),...indices])].sort((a,b)=>a-b);
  });
  let deliverScrollOffset: (() => void) | undefined;
  const virtualizer = createVirtualizer<HTMLElement, Element>({
    get count() { return large() ? props.items.length : 0; },
    getScrollElement: () => scrollHost() ?? null,
    get scrollMargin() { return origin(); },
    initialOffset: () => scrollHost()?.scrollTop ?? 0,
    estimateSize: index => {
      const estimate = props.estimateSize;
      return typeof estimate === "function" ? estimates()?.[index] ?? 48 : estimate ?? 48;
    },
    observeElementOffset: (instance, update) => {
      if (instance.scrollElement === document.scrollingElement) {
        const scroller = instance.scrollElement;
        const deliver = () => { measureOrigin(); update(scroller?.scrollTop ?? 0, false); };
        window.addEventListener("scroll", deliver, { passive: true });
        deliver();
        return () => window.removeEventListener("scroll", deliver);
      }
      const scroller = instance.scrollElement;
      deliverScrollOffset = () => {
        if (!scroller) return;
        measureOrigin(); update(scrollportMotionForViewport(scroller)?.offsetY() ?? scroller.scrollTop, false);
      };
      const stop = observeTranscriptScrollOffset(instance, (offset, scrolling) => {
        measureOrigin(); update(offset, scrolling);
      });
      return () => { deliverScrollOffset = undefined; stop(); };
    },
    get getItemKey() { return getItemKey(); },
    get initialRect() {
      const el = scrollHost();
      return {
        height: el?.clientHeight || (typeof window !== "undefined" ? window.innerHeight : 800),
        width: el?.clientWidth || (typeof window !== "undefined" ? window.innerWidth : 640),
      };
    },
    observeElementRect: (instance, update) => {
      const deliver = (rect: { width: number; height: number }) => {
        if (rect.width > 0 && rect.height > 0) update(rect);
      };
      if (instance.scrollElement === document.scrollingElement) {
        const onResize = () => deliver({ width: window.innerWidth, height: window.innerHeight });
        window.addEventListener("resize", onResize);
        onResize();
        return () => window.removeEventListener("resize", onResize);
      }
      if (instance.scrollElement && instance.scrollElement.clientHeight > 0 && instance.scrollElement.clientWidth > 0) {
        deliver({
          width: instance.scrollElement.clientWidth,
          height: instance.scrollElement.clientHeight,
        });
      }
      return observeElementRect(instance, deliver);
    },
    get overscan() { return props.overscan ?? (scrollsAncestor() ? 12 : 6); },
    get rangeExtractor() { return rangeExtractor(); },
    scrollToFn: (offset, options) => {
      const scroller = scrollHost();
      const writer = scroller ? scrollportMotionForViewport(scroller) : undefined;
      const adjustments = options.adjustments;
      // Height changes above the reader shift the reading position.
      if (writer && adjustments !== undefined) writer.commitShift(adjustments, offset, "layout_compensation");
      else if (writer && options.behavior === "smooth") void writer.revealOffset(offset);
      else if (writer) writer.commit(offset, "reveal");
      else scroller?.scrollTo({ top: offset + (adjustments ?? 0) });
    },
  });
  const values = createMemo(() => props.items.map(value => ({ value })));
  const rows = createMemo(() => {
    const current = values();
    return virtualizer.getVirtualItems().flatMap(item => {
      const entry = current[item.index];
      return entry ? [{ key: String(item.key), item, value: entry.value }] : [];
    });
  });
  bindVirtualListFind({ items: () => props.items, host, enabled: () => large() && Boolean(props.corpus || props.search),
    ownsCorpus: !props.externalFind,
    corpus: props.corpus, search: props.search,
    reveal: (index, match) => {
      virtualizer.scrollToIndex(index, { align: "center" });
      queueMicrotask(() => { const item = props.items[index]; if (item) props.onFindReveal?.(item, match); });
    },
  });
  onMount(() => props.onReveal?.(key => {
    const index = props.items.findIndex(item => props.keyOf(item) === key);
    if (index >= 0 && large()) virtualizer.scrollToIndex(index, { align: "center" });
  }));
  onCleanup(() => props.onReveal?.(undefined));
  // The mounted rows in document order: virtual rows carry their key, flow rows follow `shown`.
  const rowElements = (shown: readonly string[]): { key: string; element: HTMLElement }[] => {
    const flow = flowHost();
    if (!flow) {
      // Rows sit directly in the extent box; nested lists keep their own rows.
      return [...(host()?.firstElementChild?.children ?? [])].flatMap(element => {
        const key = element instanceof HTMLElement ? element.dataset.virtualKey : undefined;
        return key === undefined ? [] : [{ key, element: element as HTMLElement }];
      });
    }
    return [...flow.children].flatMap((element, index) => {
      const key = shown[index];
      return element instanceof HTMLElement && key !== undefined ? [{ key, element }] : [];
    });
  };
  const readingLine = (scroller: HTMLElement) => scroller.getBoundingClientRect().top + scroller.clientTop;
  const listEnd = (): number | undefined => {
    const flow = flowHost();
    if (!flow) return host()?.getBoundingClientRect().bottom;
    const last = flow.lastElementChild;
    return last ? last.getBoundingClientRect().bottom : undefined;
  };
  const captureReadingAnchor = (shown: readonly string[]): ReadingAnchor | undefined => {
    const scroller = scrollHost();
    if (!scroller) return undefined;
    const line = readingLine(scroller);
    const rows = rowElements(shown);
    const first = rows[0];
    // A reader above the list sees nothing move when rows change beneath them.
    if (!first || (first.key === shown[0] && first.element.getBoundingClientRect().top >= line)) return undefined;
    for (const row of rows) {
      const box = row.element.getBoundingClientRect();
      if (box.bottom > line) return { key: row.key, top: box.top - line };
    }
    const end = listEnd();
    return end === undefined ? undefined : { end: end - line };
  };
  const anchoredTop = (anchor: ReadingAnchor, scroller: HTMLElement, line: number): number | undefined => {
    if ("end" in anchor) {
      const end = listEnd();
      return end === undefined ? undefined : end - line;
    }
    const mounted = rowElements(keys()).find(row => row.key === anchor.key);
    if (mounted) return mounted.element.getBoundingClientRect().top - line;
    if (!large()) return undefined;
    // The new range no longer mounts the row; its measured start still places it.
    const index = keys().indexOf(anchor.key);
    virtualizer.getTotalSize();
    const item = virtualizer.measurementsCache[index];
    const offset = scrollportMotionForViewport(scroller)?.offsetY() ?? scroller.scrollTop;
    return item ? item.start - offset : undefined;
  };
  let pendingAnchor: ReadingAnchor | undefined;
  createComputed(on(keys, (_next, shown) => {
    // Render effects have not applied the new rows, so the document still shows `shown`.
    pendingAnchor = keepsReadingPosition() && shown ? captureReadingAnchor(shown) : undefined;
  }, { defer: true }));
  createEffect(on(keys, () => {
    const anchor = pendingAnchor;
    pendingAnchor = undefined;
    const scroller = scrollHost();
    const motion = scroller ? scrollportMotionForViewport(scroller) : undefined;
    if (!anchor || !scroller || !motion) return;
    const top = anchoredTop(anchor, scroller, readingLine(scroller));
    const before = "end" in anchor ? anchor.end : anchor.top;
    if (top === undefined || Math.abs(top - before) < 0.5) return;
    motion.shiftContent(top - before, motion.offsetY());
    // Mount the range at the carried offset before the frame paints.
    deliverScrollOffset?.();
  }, { defer: true }));
  createEffect(() => {
    const element = host(); if (!element) return;
    if (scrollsAncestor()) {
      let ancestor = element.parentElement;
      while (ancestor && !/(auto|scroll)/.test(getComputedStyle(ancestor).overflowY)) ancestor = ancestor.parentElement;
      setScrollHost(ancestor ?? document.scrollingElement as HTMLElement);
      measureOrigin();
      const cleanups: (() => void)[] = [];
      cleanups.push(observeSharedContentBox(element, measureOrigin));
      element.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, measureOrigin);
      cleanups.push(() => element.removeEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, measureOrigin));
      for (let parent: HTMLElement | null = element; parent; parent = parent.parentElement) {
        cleanups.push(observeSharedContentBox(parent, measureOrigin));
        parent.addEventListener("toggle", measureOrigin);
        parent.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, measureOrigin);
        const observed = parent;
        cleanups.push(() => {
          observed.removeEventListener("toggle", measureOrigin);
          observed.removeEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, measureOrigin);
        });
        if (parent === scrollHost()) break;
      }
      queueMicrotask(measureOrigin);
      const frame = requestAnimationFrame(measureOrigin);
      cleanups.push(() => cancelAnimationFrame(frame));
      onCleanup(() => { cleanups.forEach(stop => stop()); setScrollHost(undefined); });
      return;
    }
    setScrollHost(element);
    onCleanup(() => setScrollHost(undefined));
  });
  const trackHost = (element: HTMLDivElement) => { setHost(element); onCleanup(() => setHost(undefined)); };
  const trackFlowHost = (element: HTMLDivElement) => {
    setFlowHost(element); setHost(element);
    onCleanup(() => { setFlowHost(undefined); setHost(undefined); });
  };
  const flowRows = () => <KeyedIndex each={props.items} keyOf={props.keyOf}>{props.children}</KeyedIndex>;
  const VirtualRow = (rowProps: { row: () => ReturnType<typeof rows>[number] }) => {
    const row = rowProps.row;
    // Position updates leave the row's content memo unchanged.
    const value = createMemo(() => row().value);
    return <div
      data-index={row().item.index}
      data-virtual-key={row().key}
      onFocusIn={() => setFocusedKey(row().key)}
      onFocusOut={event => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocusedKey(undefined); }}
      ref={element => {
        // Observer measurements avoid forcing layout during mounting.
        onMount(() => {
          onCleanup(observeSharedContentBox(element,box=>{
            const height=box.borderHeight ?? box.height;
            if(height>0) { virtualizer.resizeItem(row().item.index,height); props.onMeasure?.(value(), element); }
          }));
        });
        if (props.externalFind && !props.externalFind(value())) onCleanup(registerFindRevealHost({
          id: `virtual-row:${crypto.randomUUID()}`, hostEl: () => element, ownsCorpus: true,
          isCollapsed: () => false, revealForFind: () => () => {}, remoteMatches: () => [],
        }));
      }}
      style={{ position: "absolute", top: "0", width: "100%", transform: `translateY(${row().item.start - origin()}px)` }}>
      {props.children(value)}
    </div>;
  };
  const renderedRows = () => (
    <KeyedIndex each={rows()} keyOf={row => row.key}>{row => <VirtualRow row={row} />}</KeyedIndex>
  );
  const extentStyle = () => ({ height: `${virtualizer.getTotalSize()}px`, position: "relative" as const });
  return <Show when={large()} fallback={
    <Show when={keepsReadingPosition()} fallback={flowRows()}>
      <div ref={trackFlowHost} style={{ display: "contents" }}>{flowRows()}</div>
    </Show>
  }>
    <Show when={scrollsAncestor()} fallback={
      <Scrollport style={{ height: "min(26rem, 50vh)" }} viewportRef={trackHost}
        viewport={{ "aria-label": props.label, tabIndex: 0, "data-testid": "virtual-card-list", style: { "overflow-anchor": "none" } }}
        content={{ get style() { return extentStyle(); } }}>
        {renderedRows()}
      </Scrollport>
    }>
      <div ref={trackHost} aria-label={props.label} data-testid="virtual-card-list"
        style={{ "overflow-anchor": "none", position: "relative" }}>
        <div style={extentStyle()}>{renderedRows()}</div>
      </div>
    </Show>
  </Show>;
}
