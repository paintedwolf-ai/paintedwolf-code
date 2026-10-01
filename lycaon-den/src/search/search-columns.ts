import type { SearchHit } from "../api/types.ts";
import type { ColumnSpec } from "../list/list-columns.ts";
import { compareNumbers, compareStrings } from "../list/list-sort.ts";
import { searchHitDisplay } from "./search-hit-display.ts";

/** Surface id for persisted pane size, column widths, and sort. */
export const SEARCH_LIST_SURFACE = "search";

function locationOf(hit: SearchHit): string {
  const display = searchHitDisplay(hit);
  const path = display.openPath;
  if (path) return path.line != null ? `${path.path}:${path.line}` : path.path;
  return display.openUrl ?? display.context ?? "";
}

function timestampOf(hit: SearchHit): number {
  const ms = hit.created_at ? Date.parse(hit.created_at) : Number.NaN;
  return Number.isFinite(ms) ? ms : 0;
}

/** With no active sort column, results retain host relevance order. */
export const SEARCH_COLUMNS: readonly ColumnSpec<SearchHit>[] = [
  {
    key: "kind",
    label: "Kind",
    width: { basis: 96, min: 72, max: 160 },
    compare: (left, right, direction) =>
      compareStrings(
        searchHitDisplay(left).kindLabel,
        searchHitDisplay(right).kindLabel,
        direction,
      ),
  },
  {
    key: "title",
    label: "Result",
    width: { basis: 260, min: 180, max: 640, grow: true },
    compare: (left, right, direction) =>
      compareStrings(
        searchHitDisplay(left).title,
        searchHitDisplay(right).title,
        direction,
      ),
  },
  {
    key: "location",
    label: "Location",
    width: { basis: 200, min: 96, max: 480 },
    // First to go on a narrow stage — paths are the widest, least scannable
    // cell, and the detail pane shows the full one.
    optional: true,
    compare: (left, right, direction) =>
      compareStrings(locationOf(left), locationOf(right), direction),
  },
  {
    key: "age",
    label: "Age",
    width: { basis: 76, min: 60, max: 140 },
    align: "end",
    firstDirection: "desc",
    compare: (left, right, direction) =>
      compareNumbers(timestampOf(left), timestampOf(right), direction),
  },
];
