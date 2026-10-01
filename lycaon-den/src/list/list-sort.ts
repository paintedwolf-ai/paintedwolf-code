/** Shared sorting for columned list surfaces. */

export type SortDirection = "asc" | "desc";

/** Active sort, or `null` for the surface's natural order (relevance, severity). */
export type SortState = { key: string; dir: SortDirection } | null;

function oppositeDirection(dir: SortDirection): SortDirection {
  return dir === "asc" ? "desc" : "asc";
}

export function compareStrings(
  left: string,
  right: string,
  direction: SortDirection,
): number {
  const cmp = left.localeCompare(right, undefined, { sensitivity: "base" });
  return direction === "asc" ? cmp : -cmp;
}

export function compareNumbers(
  left: number,
  right: number,
  direction: SortDirection,
): number {
  const cmp = left - right;
  return direction === "asc" ? cmp : -cmp;
}

/** Cycles natural direction, opposite direction, then default order. */
export function nextSortState(
  current: SortState,
  key: string,
  firstDirection: SortDirection,
): SortState {
  if (current?.key !== key) return { key, dir: firstDirection };
  if (current.dir === firstDirection) {
    return { key, dir: oppositeDirection(firstDirection) };
  }
  return null;
}

export function sortAriaValue(
  state: SortState,
  key: string,
): "ascending" | "descending" | "none" {
  if (state?.key !== key) return "none";
  return state.dir === "asc" ? "ascending" : "descending";
}

/** Parses persisted sort preferences. */
export function toSortState(
  key: string | undefined,
  dir: string | undefined,
): SortState {
  if (!key?.trim()) return null;
  if (dir !== "asc" && dir !== "desc") return null;
  return { key: key.trim(), dir };
}
