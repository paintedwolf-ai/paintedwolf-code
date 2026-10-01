import { observeScrollportOffset } from "../../platform/scrolling/scrollport-offset.ts";
import { cancelScrollportFrame, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";

/** Publishes the latest native or committed offset after geometry settles. */
export function observeVirtualScrollOffset(
  el: HTMLElement,
  notify: (offset: number, scrolling: boolean) => void,
): () => void {
  let pendingTop = el.scrollTop;
  const flush = () => notify(scrollportMotionForViewport(el)?.offsetY() ?? pendingTop, true);
  const changed = (offset: number) => {
    pendingTop = offset;
    scheduleScrollportFrame(el, "render", flush);
  };
  const stopOffset = observeScrollportOffset(el, changed);
  const stopCommits = scrollportMotionForViewport(el)?.subscribeCommits((_source, position) => changed(position.top));
  notify(pendingTop, false);
  return () => {
    stopOffset();
    stopCommits?.();
    cancelScrollportFrame(el, flush);
  };
}

export type FixedVirtualRow = {
  index: number;
  start: number;
  size: number;
};

export type FixedVirtualWindow = {
  startIndex: number;
  endIndex: number;
};

/** Whether the window has a slot for every row from `start` up to `end`. */
export function windowCoversRows(window: FixedVirtualWindow | null, start: number, end: number): boolean {
  return window !== null && start >= window.startIndex && end - 1 <= window.endIndex;
}

export type RenderedRow<Row> = { position: FixedVirtualRow; row: Row };

/** Missing positions retain their displayed rows during active navigation. */
export function renderWindowRows<Row>(
  positions: readonly FixedVirtualRow[],
  rowAt: (index: number) => Row | undefined,
  identity: (row: Row) => string,
  previous: ReadonlyMap<string, RenderedRow<Row>> | undefined,
  retain: boolean,
): Map<string, RenderedRow<Row>> {
  const rows = new Map<string, RenderedRow<Row>>();
  const unloaded: FixedVirtualRow[] = [];
  for (const position of positions) {
    const row = rowAt(position.index);
    if (row) rows.set(identity(row), { position, row });
    else unloaded.push(position);
  }
  if (!retain || !previous || unloaded.length === 0) return rows;
  const previousByIndex = new Map<number, Row>();
  for (const entry of previous.values()) previousByIndex.set(entry.position.index, entry.row);
  for (const position of unloaded) {
    const row = previousByIndex.get(position.index);
    if (row === undefined) continue;
    const key = identity(row);
    if (!rows.has(key)) rows.set(key, { position, row });
  }
  return rows;
}

type FixedVirtualWindowOptions = {
  count: number;
  scrollTop: number;
  viewportHeight: number;
  rowHeight: number;
  bufferViewports: number;
  previous?: FixedVirtualWindow;
};

/** Reuses a buffered window until the viewport reaches its guard rows. */
export function fixedVirtualWindow(
  opts: FixedVirtualWindowOptions,
): FixedVirtualWindow | null {
  const { count, rowHeight } = opts;
  if (count <= 0 || rowHeight <= 0) return null;
  const viewportHeight = Math.max(rowHeight, opts.viewportHeight);
  const safeTop = Math.max(0, opts.scrollTop);
  const visibleStart = Math.max(
    0,
    Math.min(count - 1, Math.floor(safeTop / rowHeight)),
  );
  const visibleEnd = Math.max(
    visibleStart,
    Math.min(count - 1, Math.ceil((safeTop + viewportHeight) / rowHeight) - 1),
  );
  const visibleRows = Math.max(1, Math.ceil(viewportHeight / rowHeight));
  const bufferRows = Math.max(
    1,
    Math.ceil(visibleRows * Math.max(0, opts.bufferViewports)),
  );
  // Guard rows fit inside the buffer so nearby windows remain reusable.
  const guardRows = Math.min(bufferRows, Math.max(1, Math.ceil(visibleRows / 2)));
  const previous = opts.previous;
  if (
    previous &&
    previous.startIndex >= 0 &&
    previous.endIndex < count &&
    (previous.startIndex === 0 ||
      visibleStart >= previous.startIndex + guardRows) &&
    (previous.endIndex === count - 1 ||
      visibleEnd <= previous.endIndex - guardRows)
  ) {
    return previous;
  }
  const next = {
    startIndex: Math.max(0, visibleStart - bufferRows),
    endIndex: Math.min(count - 1, visibleEnd + bufferRows),
  };
  return previous && previous.startIndex === next.startIndex && previous.endIndex === next.endIndex
    ? previous
    : next;
}

export function fixedVirtualRows(
  window: FixedVirtualWindow | null,
  rowHeight: number,
): FixedVirtualRow[] {
  if (!window || rowHeight <= 0) return [];
  const count = window.endIndex - window.startIndex + 1;
  const rows = new Array<FixedVirtualRow>(Math.max(0, count));
  for (let index = window.startIndex; index <= window.endIndex; index += 1) {
    rows[index - window.startIndex] = {
      index,
      start: index * rowHeight,
      size: rowHeight,
    };
  }
  return rows;
}

/** Scroll offset that reveals `index`, or `null` when it is already on screen. */
export function scrollTopToRevealRow(args: {
  index: number;
  rowHeight: number;
  scrollTop: number;
  viewportHeight: number;
  contentHeight: number;
  stickyHeightAt: (scrollTop: number) => number;
}): number | null {
  const { index, rowHeight, scrollTop, viewportHeight, contentHeight } = args;
  if (index < 0 || rowHeight <= 0) return null;
  const rowTop = index * rowHeight;
  const insetNow = args.stickyHeightAt(scrollTop);
  const y = rowTop - scrollTop;
  if (y >= insetNow && y + rowHeight <= viewportHeight) return null;

  let top: number;
  if (y + rowHeight > viewportHeight) {
    top = Math.max(0, rowTop + rowHeight - viewportHeight);
  } else {
    top = Math.max(0, rowTop - insetNow);
    for (let i = 0; i < 6; i++) {
      const inset = args.stickyHeightAt(top);
      const next = Math.max(0, rowTop - inset);
      if (Math.abs(next - top) < 1) {
        top = next;
        break;
      }
      top = next;
    }
  }
  const maxScroll = Math.max(0, contentHeight - viewportHeight);
  return Math.min(top, maxScroll);
}

/** Scroll offset that places `index` at the top of its available tree area. */
export function scrollTopToAlignRowAtStart(args: {
  index: number;
  rowHeight: number;
  viewportHeight: number;
  contentHeight: number;
  stickyHeightAt: (scrollTop: number) => number;
}): number | null {
  const { index, rowHeight, viewportHeight, contentHeight } = args;
  if (index < 0 || rowHeight <= 0) return null;
  const rowTop = index * rowHeight;
  let top = Math.max(0, rowTop);
  for (let i = 0; i < 6; i++) {
    const inset = args.stickyHeightAt(top);
    const next = Math.max(0, rowTop - inset);
    if (Math.abs(next - top) < 1) {
      top = next;
      break;
    }
    top = next;
  }
  const maxScroll = Math.max(0, contentHeight - viewportHeight);
  return Math.min(top, maxScroll);
}

/** Scroll offset that places `index` in the center of the available tree area. */
export function scrollTopToAlignRowCentered(args: {
  index: number;
  rowHeight: number;
  viewportHeight: number;
  contentHeight: number;
  stickyHeightAt: (scrollTop: number) => number;
}): number | null {
  const { index, rowHeight, viewportHeight, contentHeight } = args;
  if (index < 0 || rowHeight <= 0) return null;
  const rowTop = index * rowHeight;
  let top = Math.max(0, rowTop - Math.max(0, (viewportHeight - rowHeight) / 2));
  for (let i = 0; i < 6; i++) {
    const inset = args.stickyHeightAt(top);
    const available = Math.max(rowHeight, viewportHeight - inset);
    const targetOffset = inset + (available - rowHeight) / 2;
    const next = Math.max(0, rowTop - targetOffset);
    if (Math.abs(next - top) < 1) {
      top = next;
      break;
    }
    top = next;
  }
  const maxScroll = Math.max(0, contentHeight - viewportHeight);
  return Math.min(top, maxScroll);
}
