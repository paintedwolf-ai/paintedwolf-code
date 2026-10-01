import { expect, it } from "vitest";
import { searchCoverageNote, searchIssueNotes, sourceIndexNotes } from "./search-status.ts";
import { searchWarming } from "./search-refresh.ts";

it("combines incomplete search and file coverage into one indexing note", () => {
  expect(searchCoverageNote({ exhaustive: false, issues: [
    { executor: "code", reason: "catalog_incomplete" },
    { executor: "code", reason: "catalog_bounded", count: 3 },
    { executor: "code", reason: "time_budget" },
    { executor: "code", reason: "result_limit" },
  ] }, [
    { root_id: "r", state: "ready", discovery_complete: false, refreshing: true, bounded_directories: 3, failed_directories: 0 },
  ])).toBe("Results may be incomplete. Still indexing…");
});

it("does not imply permanent coverage gaps will finish indexing", () => {
  expect(searchCoverageNote({ issues: [{ executor: "code", reason: "catalog_bounded" }] }))
    .toBe("Results may be incomplete.");
  expect(searchCoverageNote({ exhaustive: false })).toBe("Results may be incomplete.");
  expect(searchCoverageNote({ issues: [{ executor: "code", reason: "files_skipped" }] }))
    .toBe("Results may be incomplete.");
});

it("keeps failures visible even while another source is indexing", () => {
  expect(searchCoverageNote({ issues: [{ executor: "code", reason: "index_warming" }] }, [
    { root_id: "r", state: "failed", discovery_complete: false, refreshing: false, bounded_directories: 0, failed_directories: 1 },
  ])).toBe("Some results are unavailable. Try again.");
});

it("shows search limits but keeps routine refreshes and complete results quiet", () => {
  expect(searchCoverageNote({ issues: [
    { executor: "code", reason: "time_budget" }, { executor: "code", reason: "result_limit" },
  ] })).toBe("Results may be incomplete. Narrow your search for more matches.");
  expect(searchCoverageNote({ exhaustive: false, issues: [{ executor: "code", reason: "catalog_refreshing" }] }))
    .toBe("");
  expect(searchCoverageNote(null, [
    { root_id: "r", state: "ready", discovery_complete: true, refreshing: true, bounded_directories: 0, failed_directories: 0 },
  ])).toBe("");
  expect(searchCoverageNote({ exhaustive: true, issues: [] })).toBe("");
  expect(searchCoverageNote()).toBe("");
});

it("distinguishes permanent coverage gaps from work that can finish", () => {
  const issues = [
    { executor: "code", reason: "catalog_bounded" as const, count: 2 },
    { executor: "code", reason: "catalog_failed" as const, count: 1 },
    { executor: "code", reason: "catalog_refresh_failed" as const, count: 1 },
  ];
  expect(searchWarming({ issues })).toBe(false);
  const note = searchIssueNotes(issues);
  expect(note).toContain("2 folders");
  expect(note).toContain("could not be read");
  expect(note).toContain("could not be refreshed");
  expect(note).not.toContain("refresh automatically");
});

it("keeps an available root's failure separate from another root's pending discovery", () => {
  const note = sourceIndexNotes([
    { root_id: "a", state: "ready", discovery_complete: true, refreshing: false, bounded_directories: 0, failed_directories: 1 },
    { root_id: "b", state: "warming", discovery_complete: false, refreshing: true, bounded_directories: 0, failed_directories: 0 },
  ]);
  expect(note).toContain("could not be read");
  expect(note).toContain("still being indexed");
});

it("explains a symbol search that stopped at its budget", () => {
  const issues = [{ executor: "symbol", reason: "symbol_budget" as const, count: 1 }];
  expect(searchCoverageNote({ exhaustive: false, issues })).toBe("Results may be incomplete. Narrow your search for more matches.");
  expect(searchIssueNotes(issues)).toContain("The symbol search stopped before checking every candidate.");
});
