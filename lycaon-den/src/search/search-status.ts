import type { SearchIssue, SearchResponse, SourceIndexRootCoverage } from "../api/types.ts";

/** Both search lanes share one coverage summary. */
export function searchCoverageNote(
  result?: Partial<Pick<SearchResponse, "issues" | "exhaustive">> | null,
  roots: readonly SourceIndexRootCoverage[] = [],
): string {
  const reasons = new Set([...(result?.issues ?? []), ...sourceIndexIssues(roots)]
    .filter(issue => (issue.count ?? 1) > 0)
    .map(issue => issue.reason));
  if (reasons.has("executor_error") || reasons.has("catalog_failed") || reasons.has("catalog_refresh_failed")) {
    return "Some results are unavailable. Try again.";
  }
  if (reasons.has("catalog_warming") || reasons.has("catalog_incomplete") || reasons.has("index_warming")) {
    return "Results may be incomplete. Still indexing…";
  }
  if (reasons.has("time_budget") || reasons.has("symbol_budget") || reasons.has("result_limit")) {
    return "Results may be incomplete. Narrow your search for more matches.";
  }
  // Background refreshes retain the previous searchable generation.
  if (reasons.size === 1 && reasons.has("catalog_refreshing")) return "";
  if (result?.exhaustive === false || reasons.has("catalog_bounded") || reasons.has("files_skipped")) {
    return "Results may be incomplete.";
  }
  return "";
}

/** Coverage explanations stay visible alongside useful results. */
export function searchIssueNotes(issues: readonly SearchIssue[] = []): string {
  const count = (reason: SearchIssue["reason"]) => issues
    .filter(issue => issue.reason === reason)
    .reduce((total, issue) => total + (issue.count ?? 1), 0);
  const has = (reason: SearchIssue["reason"]) => count(reason) > 0;
  const quantity = (reason: SearchIssue["reason"], noun: string) => `${count(reason)} ${noun}${count(reason) === 1 ? "" : "s"}`;
  const notes: string[] = [];
  if (has("executor_error")) notes.push("Some results are unavailable because a search failed. Try again.");
  if (has("catalog_refresh_failed")) notes.push("Some results could not be refreshed. Try again.");
  if (has("catalog_failed")) notes.push(`${quantity("catalog_failed", "folder")} could not be read; some matches may be missing.`);
  if (has("catalog_bounded")) notes.push(`${quantity("catalog_bounded", "folder")} exceeded the indexing budget; some matches may be missing.`);
  if (has("files_skipped")) notes.push(`${quantity("files_skipped", "file")} could not be searched because of availability, format, or size limits.`);
  if (has("catalog_warming") || has("catalog_incomplete") || has("index_warming")) {
    notes.push("Files are still being indexed. Results refresh automatically while this view is open.");
  } else if (has("catalog_refreshing")) {
    notes.push("Files are being updated. Results refresh automatically while this view is open.");
  }
  if (has("time_budget")) notes.push("The search reached its time limit. Narrow the query to cover more files.");
  if (has("symbol_budget")) notes.push("The symbol search stopped before checking every candidate. Type more of the name to find more declarations.");
  if (has("result_limit")) notes.push("The result limit was reached. Narrow the query to see additional matches.");
  return notes.join(" ");
}

export function sourceIndexNotes(roots: readonly SourceIndexRootCoverage[] = []): string {
  return searchIssueNotes(sourceIndexIssues(roots));
}

function sourceIndexIssues(roots: readonly SourceIndexRootCoverage[]): SearchIssue[] {
  const issues: SearchIssue[] = [];
  for (const root of roots) {
    const add = (reason: SearchIssue["reason"], count = 1) => issues.push({ executor: "code", reason, count });
    if (root.error || root.state === "failed") add(root.state === "ready" ? "catalog_refresh_failed" : "executor_error");
    else if (!root.discovery_complete) add(root.state === "ready" ? "catalog_incomplete" : "catalog_warming");
    else if (root.refreshing) add("catalog_refreshing");
    if (root.bounded_directories) add("catalog_bounded", root.bounded_directories);
    if (root.failed_directories) add("catalog_failed", root.failed_directories);
  }
  return issues;
}
