import { afterEach, expect, it, vi } from "vitest";
import type { SearchResponse } from "../api/types.ts";
import { createSearchRefresh, searchWarming } from "./search-refresh.ts";

afterEach(() => vi.useRealTimers());

it("continues slow indexing at bounded frequency and stops on completion", () => {
  vi.useFakeTimers();
  const refresh = createSearchRefresh();
  const result = { issues: [{ reason: "catalog_warming" }] } as SearchResponse;
  const rerun = vi.fn(() => refresh.schedule(result, rerun));
  refresh.schedule(result, rerun);
  vi.advanceTimersByTime(120_000);
  expect(rerun.mock.calls.length).toBeGreaterThan(20);
  expect(rerun.mock.calls.length).toBeLessThan(30);
  refresh.schedule({ issues: [] } as unknown as SearchResponse, rerun);
  vi.advanceTimersByTime(30_000);
  expect(vi.getTimerCount()).toBe(0);
});

it("cancels content-index retries when the query or visible surface changes", () => {
  vi.useFakeTimers();
  const refresh = createSearchRefresh();
  const rerun = vi.fn();
  const result = { issues: [{ reason: "index_warming" }] } as SearchResponse;
  refresh.schedule(result, rerun);
  refresh.clear();
  vi.advanceTimersByTime(30_000);
  expect(rerun).not.toHaveBeenCalled();
  refresh.reset();
  refresh.schedule(result, rerun);
  vi.advanceTimersByTime(750);
  expect(rerun).toHaveBeenCalledTimes(1);
});

it("keeps refreshing while a readable generation is incomplete or updating", () => {
  const filling = { issues: [{ reason: "catalog_incomplete" }] } as SearchResponse;
  const nothing = { issues: [{ reason: "catalog_warming" }] } as SearchResponse;
  expect(searchWarming(filling)).toBe(true);
  expect(searchWarming(nothing)).toBe(true);
  expect(searchWarming({ issues: [{ executor: "code", reason: "catalog_refreshing" }] })).toBe(true);
});

it("schedules a retry for a partial generation so late hits arrive on their own", () => {
  vi.useFakeTimers();
  const refresh = createSearchRefresh();
  const rerun = vi.fn();
  refresh.schedule({ issues: [{ reason: "catalog_incomplete" }] } as SearchResponse, rerun);
  vi.advanceTimersByTime(750);
  expect(rerun).toHaveBeenCalledTimes(1);
});
