/** Keeps the browser's physical scroll extent bounded while retaining logical rank. */
export class SegmentedScroll {
  private origin = 0;
  constructor(readonly rowHeight: number, readonly physicalLimit = 4_000_000) {
    if (!Number.isFinite(rowHeight) || rowHeight <= 0 || !Number.isFinite(physicalLimit) || physicalLimit < rowHeight * 10) {
      throw new Error("Invalid presentation scroll geometry.");
    }
  }
  extent(rows: number): number { return Math.min(this.physicalLimit, rows * this.rowHeight); }
  logicalOffset(scrollTop: number): number { return this.origin + scrollTop; }
  physicalOffset(logical: number): number { return logical - this.origin; }
  row(scrollTop: number): number { return Math.floor(this.logicalOffset(scrollTop) / this.rowHeight); }

  reveal(row: number, withinRow: number, rows: number, viewportHeight: number): number {
    const total = rows * this.rowHeight;
    const target = Math.max(0, Math.min(Math.max(0, total - viewportHeight), row * this.rowHeight + withinRow));
    const extent = this.extent(rows);
    this.origin = Math.max(0, Math.min(Math.max(0, total - extent), target - extent / 2));
    return target - this.origin;
  }

  recenter(scrollTop: number, rows: number, viewportHeight: number): number {
    const extent = this.extent(rows);
    if (rows * this.rowHeight <= this.physicalLimit) { this.origin = 0; return scrollTop; }
    if (scrollTop > extent / 4 && scrollTop < extent * 3 / 4 - viewportHeight) return scrollTop;
    const logical = this.logicalOffset(scrollTop);
    return this.reveal(Math.floor(logical / this.rowHeight), logical % this.rowHeight, rows, viewportHeight);
  }
}
