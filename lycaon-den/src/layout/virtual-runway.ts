type VirtualRange = {
  startIndex: number;
  endIndex: number;
  overscan: number;
  count: number;
};

type BufferedVirtualRangeOptions = {
  range: VirtualRange;
  bufferPx: number;
  rowSize: (index: number) => number;
  gapPx?: number;
};

/** Expands a virtual range until both row and painted-pixel runways are met. */
export function bufferedVirtualRange(
  opts: BufferedVirtualRangeOptions,
): number[] {
  const { range } = opts;
  if (range.count <= 0) return [];

  const visibleStart = Math.max(0, Math.min(range.startIndex, range.count - 1));
  const visibleEnd = Math.max(
    visibleStart,
    Math.min(range.endIndex, range.count - 1),
  );
  const minimumRows = Math.max(0, Math.floor(range.overscan));
  const bufferPx = Math.max(0, opts.bufferPx);
  const gapPx = Math.max(0, opts.gapPx ?? 0);
  const rowExtent = (index: number): number => {
    const size = opts.rowSize(index);
    return (Number.isFinite(size) ? Math.max(0, size) : 0) + gapPx;
  };

  let start = visibleStart;
  let beforePx = 0;
  while (
    start > 0 &&
    (visibleStart - start < minimumRows || beforePx < bufferPx)
  ) {
    start -= 1;
    beforePx += rowExtent(start);
  }

  let end = visibleEnd;
  let afterPx = 0;
  while (
    end < range.count - 1 &&
    (end - visibleEnd < minimumRows || afterPx < bufferPx)
  ) {
    end += 1;
    afterPx += rowExtent(end);
  }

  return Array.from({ length: end - start + 1 }, (_, offset) => start + offset);
}
