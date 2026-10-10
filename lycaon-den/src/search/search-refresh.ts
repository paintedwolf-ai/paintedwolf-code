import type { SearchResponse } from "../api/types.ts";

/** Indexing can fill missing generations and incomplete tree coverage. */
export function searchWarming(result: Pick<SearchResponse, "issues">): boolean {
  return result.issues?.some((issue) =>
    issue.reason === "catalog_warming" ||
    issue.reason === "catalog_incomplete" ||
    issue.reason === "catalog_refreshing" ||
    issue.reason === "index_warming",
  ) ?? false;
}

/** Detects an incomplete interactive response. */
export function searchNeedsRefinement(result: Pick<SearchResponse, "issues">): boolean {
  return searchWarming(result) || (result.issues?.some((issue) =>
    issue.reason === "result_limit" || issue.reason === "time_budget" || issue.reason === "symbol_pending",
  ) ?? false);
}

/** Pending symbols retain a frontier and can advance without background indexing. */
function searchCanAdvance(result: Pick<SearchResponse, "issues">): boolean {
  return searchWarming(result) || (result.issues?.some((issue) =>
    issue.reason === "symbol_pending",
  ) ?? false);
}

/** Retries one visible query while work can advance. */
export function createSearchRefresh() {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let attempt = 0;
  const clear = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  const reset = () => { clear(); attempt = 0; };
  const schedule = (result: Pick<SearchResponse, "issues">, rerun: () => void) => {
    clear();
    if (!searchCanAdvance(result)) { reset(); return; }
    const delay = Math.min(750 * 2 ** attempt, 5000);
    attempt = Math.min(attempt + 1, 3);
    timer = setTimeout(() => { timer = undefined; rerun(); }, delay);
  };
  return { clear, reset, schedule };
}
