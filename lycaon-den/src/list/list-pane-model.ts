import {
  FILES_TREE_WIDTH_DEFAULT_PX,
  FILES_TREE_WIDTH_MIN_PX,
  LIST_PANE_HEIGHT_DEFAULT_PX,
  LIST_PANE_HEIGHT_MAX_PX,
  LIST_PANE_HEIGHT_MIN_PX,
  LIST_PANE_WIDTH_DEFAULT_PX,
  LIST_PANE_WIDTH_MAX_PX,
  LIST_PANE_WIDTH_MIN_PX,
  type DenListPanePrefs,
  type DenLayoutPrefs,
} from "../../shared/app-state-types.ts";
import { type ColumnSpec, pruneColumnWidths } from "./list-columns.ts";
import { toSortState, type SortState } from "./list-sort.ts";

export type ListPaneAxis = "width" | "height";

/** Files tree uses custom width bounds. */
export const FILES_TREE_SURFACE = "files-tree";

type ListPaneBounds = {
  min: number;
  max: number;
  /** Default for missing or invalid sizes. */
  fallback: number;
};

const AXIS_BOUNDS: Record<ListPaneAxis, ListPaneBounds> = {
  width: {
    min: LIST_PANE_WIDTH_MIN_PX,
    max: LIST_PANE_WIDTH_MAX_PX,
    fallback: LIST_PANE_WIDTH_DEFAULT_PX,
  },
  height: {
    min: LIST_PANE_HEIGHT_MIN_PX,
    max: LIST_PANE_HEIGHT_MAX_PX,
    fallback: LIST_PANE_HEIGHT_DEFAULT_PX,
  },
};

const SURFACE_BOUNDS: Record<
  string,
  Partial<Record<ListPaneAxis, ListPaneBounds>>
> = {
  [FILES_TREE_SURFACE]: {
    width: {
      min: FILES_TREE_WIDTH_MIN_PX,
      max: LIST_PANE_WIDTH_MAX_PX,
      fallback: FILES_TREE_WIDTH_DEFAULT_PX,
    },
  },
};

function listPaneBounds(
  surface: string,
  axis: ListPaneAxis,
): ListPaneBounds {
  return SURFACE_BOUNDS[surface]?.[axis] ?? AXIS_BOUNDS[axis];
}

/** Caps detail panes at 60% of their container. */
export function maxListPaneSizePx(
  surface: string,
  axis: ListPaneAxis,
  bodySize?: number,
): number {
  const bounds = listPaneBounds(surface, axis);
  if (bodySize === undefined || !Number.isFinite(bodySize) || bodySize <= 0) {
    return bounds.max;
  }
  return Math.min(bounds.max, Math.max(bounds.min, Math.floor(bodySize * 0.6)));
}

export function clampListPaneSizePx(
  surface: string,
  axis: ListPaneAxis,
  value: number,
  bodySize?: number,
): number {
  const bounds = listPaneBounds(surface, axis);
  if (!Number.isFinite(value)) return bounds.fallback;
  return Math.round(
    Math.min(
      maxListPaneSizePx(surface, axis, bodySize),
      Math.max(bounds.min, value),
    ),
  );
}

function listPaneSizeDefaultPx(
  surface: string,
  axis: ListPaneAxis,
): number {
  return listPaneBounds(surface, axis).fallback;
}

export function listPaneSizeMinPx(surface: string, axis: ListPaneAxis): number {
  return listPaneBounds(surface, axis).min;
}

/** Clamps one persisted pane entry. */
export function clampListPanePrefs<T>(
  surface: string,
  prefs: DenListPanePrefs | undefined,
  cols?: readonly ColumnSpec<T>[],
): DenListPanePrefs {
  const out: DenListPanePrefs = {};
  if (typeof prefs?.widthPx === "number" && Number.isFinite(prefs.widthPx)) {
    out.widthPx = clampListPaneSizePx(surface, "width", prefs.widthPx);
  }
  if (typeof prefs?.heightPx === "number" && Number.isFinite(prefs.heightPx)) {
    out.heightPx = clampListPaneSizePx(surface, "height", prefs.heightPx);
  }
  const widths = cols
    ? pruneColumnWidths(cols, prefs?.columnWidths)
    : prefs?.columnWidths;
  if (widths && Object.keys(widths).length > 0) {
    out.columnWidths = widths;
  }
  const sort = toSortState(prefs?.sortKey, prefs?.sortDir);
  if (sort) {
    out.sortKey = sort.key;
    out.sortDir = sort.dir;
  }
  return out;
}

export function resolveListPanePrefs(
  layout: DenLayoutPrefs | undefined,
  surface: string,
): DenListPanePrefs {
  return clampListPanePrefs(surface, layout?.listPanes?.[surface]);
}

export function resolveListPaneSizePx(
  surface: string,
  prefs: DenListPanePrefs | undefined,
  axis: ListPaneAxis,
  bodySize?: number,
): number {
  const stored = axis === "width" ? prefs?.widthPx : prefs?.heightPx;
  return clampListPaneSizePx(
    surface,
    axis,
    stored ?? listPaneSizeDefaultPx(surface, axis),
    bodySize,
  );
}

export function resolveListPaneSort(
  prefs: DenListPanePrefs | undefined,
): SortState {
  return toSortState(prefs?.sortKey, prefs?.sortDir);
}
