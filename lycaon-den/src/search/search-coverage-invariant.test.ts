import { afterEach, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import type { SearchIssue, SearchIssueReason } from "../api/types.ts";
import { createSearchRefresh, searchNeedsRefinement } from "./search-refresh.ts";
import { searchIssueNotes } from "./search-status.ts";

interface Vector extends SearchIssue { retry: boolean; refine: boolean }
const vectors: Vector[] = JSON.parse(readFileSync(new URL("../../../lycaon/test/fixtures/search/coverage.json", import.meta.url), "utf8"));
// A new wire reason requires an explicit refinement and retry expectation.
const reasons: Record<SearchIssueReason, true> = {
  result_limit: true, executor_error: true, files_skipped: true, time_budget: true,
  catalog_warming: true, catalog_incomplete: true, catalog_bounded: true,
  catalog_failed: true, catalog_refresh_failed: true, catalog_refreshing: true,
  index_warming: true, symbol_pending: true, symbol_budget: true,
};

afterEach(() => vi.useRealTimers());
it("covers every typed wire coverage reason", () => {
  expect(vectors.map(v => v.reason).sort()).toEqual(Object.keys(reasons).sort());
});

for (const vector of vectors) {
  it(`preserves ${vector.reason} units and follows its retry contract`, () => {
    vi.useFakeTimers();
    const response: { issues: SearchIssue[] } = JSON.parse(JSON.stringify({ issues: [vector] }));
    const original = structuredClone(response);
    expect(searchNeedsRefinement(response)).toBe(vector.refine);
    expect(searchIssueNotes(response.issues)).not.toBe("");
    const refresh = createSearchRefresh();
    const rerun = vi.fn();
    refresh.schedule(response, rerun);
    expect(vi.getTimerCount()).toBe(vector.retry ? 1 : 0);
    vi.advanceTimersByTime(5000);
    expect(rerun).toHaveBeenCalledTimes(vector.retry ? 1 : 0);
    refresh.schedule({ issues: [] }, rerun);
    expect(vi.getTimerCount()).toBe(0);
    expect(response).toEqual(original);
    expect(response.issues[0]?.count).toBe(vector.count);
    expect(response.issues[0]?.limit).toBe(vector.limit);
  });
}
