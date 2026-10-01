import type { SortDirection, SortState } from "./list-sort.ts";

/** Shared column layout and sorting model. */

export type ColumnWidth = {
  /** Resting width in px. */
  basis: number;
  min: number;
  max: number;
  /** Absorbs leftover width. */
  grow?: boolean;
};

export type ColumnSpec<T> = {
  key: string;
  label: string;
  width: ColumnWidth;
  align?: "start" | "end";
  /** Omit for a column this surface sorts elsewhere, or not at all. */
  compare?: (left: T, right: T, direction: SortDirection) => number;
  /**
   * Whether the header offers a sort control. Defaults to having a comparator;
   * set it without one when the store does the sorting.
   */
  sortable?: boolean;
  /** Direction applied on the first click. */
  firstDirection?: SortDirection;
  /** Hidden from the header while retaining layout space. */
  headerHidden?: boolean;
  /** Droppable when the stage narrows. */
  optional?: boolean;
};

export type ColumnWidths = Record<string, number>;

export function columnFirstDirection<T>(col: ColumnSpec<T>): SortDirection {
  return col.firstDirection ?? "asc";
}

/** Whether a column's header offers a sort control. */
export function columnSortable<T>(col: ColumnSpec<T>): boolean {
  return col.sortable ?? col.compare != null;
}

export function clampColumnWidthPx(
  width: ColumnWidth,
  value: number,
): number {
  if (!Number.isFinite(value)) return width.basis;
  return Math.round(Math.min(width.max, Math.max(width.min, value)));
}

export function clampColumnWidth<T>(col: ColumnSpec<T>, value: number): number {
  return clampColumnWidthPx(col.width, value);
}

/** Stored widths merged over the spec's resting widths, each clamped. */
export function resolveColumnWidths<T>(
  cols: readonly ColumnSpec<T>[],
  stored?: ColumnWidths,
): ColumnWidths {
  const out: ColumnWidths = {};
  for (const col of cols) {
    out[col.key] = clampColumnWidth(col, stored?.[col.key] ?? col.width.basis);
  }
  return out;
}

/** Keep finite widths for current columns. */
export function pruneColumnWidths<T>(
  cols: readonly ColumnSpec<T>[],
  stored?: ColumnWidths,
): ColumnWidths | undefined {
  if (!stored) return undefined;
  const keys = new Set(cols.map((col) => col.key));
  const out: ColumnWidths = {};
  for (const [key, value] of Object.entries(stored)) {
    if (keys.has(key) && Number.isFinite(value)) out[key] = value;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/**
 * Row grid template. Fixed tracks are minmax(0, width) so a tight row shrinks
 * its cells instead of overflowing.
 */
export function columnGridTemplate<T>(
  cols: readonly ColumnSpec<T>[],
  widths: ColumnWidths,
): string {
  return cols
    .map((col) => {
      const px = widths[col.key] ?? col.width.basis;
      return col.width.grow ? `minmax(${col.width.min}px, 1fr)` : `minmax(0, ${px}px)`;
    })
    .join(" ");
}

/** Default gutter between tracks, matching `--den-list-gap`. */
export const LIST_COLUMN_GAP = 12;

/** Width needed to show every column at rest; the grow column counts at its minimum. */
export function columnTrackTotal<T>(
  cols: readonly ColumnSpec<T>[],
  widths: ColumnWidths,
  gap: number = LIST_COLUMN_GAP,
): number {
  if (cols.length === 0) return 0;
  const tracks = cols.reduce(
    (total, col) =>
      total + (col.width.grow ? col.width.min : (widths[col.key] ?? col.width.basis)),
    0,
  );
  return tracks + gap * (cols.length - 1);
}

/** Columns that fit `available`, shedding optional columns rightmost first. */
export function fitColumns<T>(
  cols: readonly ColumnSpec<T>[],
  widths: ColumnWidths,
  available: number,
  gap: number = LIST_COLUMN_GAP,
): readonly ColumnSpec<T>[] {
  // Unmeasured: show everything.
  if (!Number.isFinite(available) || available <= 0) return cols;
  let kept = cols;
  while (columnTrackTotal(kept, widths, gap) > available) {
    let shed = -1;
    for (let i = kept.length - 1; i >= 0; i--) {
      if (kept[i]?.optional) {
        shed = i;
        break;
      }
    }
    if (shed < 0) break;
    kept = [...kept.slice(0, shed), ...kept.slice(shed + 1)];
  }
  return kept;
}

/** Stable sort by the active column. */
export function sortRows<T>(
  rows: readonly T[],
  cols: readonly ColumnSpec<T>[],
  state: SortState,
): T[] {
  if (!state) return [...rows];
  const col = cols.find((c) => c.key === state.key);
  if (!col?.compare) return [...rows];
  const compare = col.compare;
  // Decorate with the original index so equal rows keep their incoming order.
  return rows
    .map((row, index) => ({ row, index }))
    .sort((a, b) => compare(a.row, b.row, state.dir) || a.index - b.index)
    .map((entry) => entry.row);
}
