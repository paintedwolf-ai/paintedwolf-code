import type { WorktreeLandOutcome } from "../chat/actions/git-actions.ts";

export function formatWorktreeLandResult(
  result: WorktreeLandOutcome,
  branch: string,
): { sentence: string; conflicts: string[] } {
  if (result.landed) {
    return {
      sentence: `Merged ${result.commits} ${result.commits === 1 ? "commit" : "commits"} into ${result.base_branch}.`,
      conflicts: [],
    };
  }
  switch (result.reason) {
    case "nothing_to_land":
      return {
        sentence: `Nothing to merge — no new commits on ${branch}.`,
        conflicts: [],
      };
    case "base_dirty":
      return {
        sentence:
          "Your project folder has uncommitted changes. Commit or stash them, then merge again.",
        conflicts: [],
      };
    case "base_on_other_branch":
      return {
        sentence: `Your project folder is on ${result.current ?? "another branch"}, not ${result.base_branch}. Switch back, then merge again.`,
        conflicts: [],
      };
    case "base_branch_missing":
      return {
        sentence: `${result.base_branch} no longer exists.`,
        conflicts: [],
      };
    case "conflict":
      return {
        sentence: "The merge conflicts and was not applied. Nothing changed.",
        conflicts: result.conflicts ?? [],
      };
    case "base_missing":
      return {
        sentence: `Your project folder checkout for ${result.base_branch} is missing.`,
        conflicts: [],
      };
    case "detached_head":
      return {
        sentence:
          "The project folder has no checked-out branch. Check out the base branch, then merge again.",
        conflicts: [],
      };
    default:
      return {
        sentence:
          "The merge could not be completed. Refresh the checkout and try again.",
        conflicts: [],
      };
  }
}
