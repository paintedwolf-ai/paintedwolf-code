import type {
  BoardGitSlice,
  BoardView,
  GitRepoEntry,
} from "../../api/types.ts";
import type { GitWorkspaceStatus } from "./git-workspace-status.ts";
import { gitReposChangeCount } from "../../components/git-repo-scope.ts";

export function boardHasGitPulse(board?: BoardView | null): board is BoardView & {
  git: BoardGitSlice;
} {
  return board?.git != null && typeof board.git.available === "boolean";
}

/** Board git slice as a workspace status. Files stay until the next status read replaces them. */
export function gitStatusFromBoardPulse(
  git: BoardGitSlice,
  previous?: GitWorkspaceStatus,
): GitWorkspaceStatus {
  const countsMatch =
    previous != null &&
    previous.dirty === git.dirty &&
    previous.staged_count === git.staged_count &&
    previous.unstaged_count === git.unstaged_count;
  return {
    available: git.available,
    repo_id: git.repo_id || previous?.repo_id || "",
    root_ids: previous?.root_ids ?? [],
    branch: git.branch,
    head_short: git.head_short,
    upstream: previous?.upstream,
    ahead: previous?.ahead ?? 0,
    behind: previous?.behind ?? 0,
    dirty: git.dirty,
    staged_count: git.staged_count,
    unstaged_count: git.unstaged_count,
    changed_count: countsMatch
      ? (previous?.changed_count ?? 0)
      : git.staged_count + git.unstaged_count,
    files: previous?.files ?? [],
  };
}

/** Skip GET /git/status when the pulse already matches a usable store row. */
export function shouldSkipGitStatusFetch(
  pulse?: BoardGitSlice,
  previous?: GitWorkspaceStatus,
): boolean {
  if (!pulse || !previous) return false;
  if (
    pulse.available !== previous.available ||
    pulse.dirty !== previous.dirty ||
    pulse.staged_count !== previous.staged_count ||
    pulse.unstaged_count !== previous.unstaged_count ||
    (pulse.branch ?? "") !== (previous.branch ?? "")
  ) {
    return false;
  }
  if (pulse.dirty && (previous.files?.length ?? 0) === 0) {
    return false;
  }
  return true;
}

/** Changed-file badge: repo-set total, else file list length, else staged+unstaged. */
export function gitChangeBadgeCount(
  status?: GitWorkspaceStatus,
  repos?: readonly GitRepoEntry[],
): number {
  if (repos && repos.length > 0) {
    return gitReposChangeCount(repos);
  }
  if (!status) return 0;
  if (status.files.length > 0) return status.files.length;
  return (status.staged_count ?? 0) + (status.unstaged_count ?? 0);
}
