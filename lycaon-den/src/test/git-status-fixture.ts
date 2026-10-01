import type { GitChangesPage, GitStatusSummary } from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";

export function gitWorkspaceStatus(overrides: Partial<GitWorkspaceStatus> = {}): GitWorkspaceStatus {
  return {
    available: true,
    repo_id: "repo",
    root_ids: [],
    ahead: 0,
    behind: 0,
    dirty: false,
    staged_count: 0,
    unstaged_count: 0,
    changed_count: 0,
    files: [],
    ...overrides,
  };
}

export function gitStatusSummary(status: GitWorkspaceStatus, revision = 1): GitStatusSummary {
  const { files: _files, ...summary } = status;
  return { ...summary, revision, refreshing: false };
}

export function gitChangesPage(status: GitWorkspaceStatus, revision = 1): GitChangesPage {
  return { repo_id: status.repo_id, revision, refreshing: false, files: status.files };
}

/** Status and changes reads that answer one published revision per repository. */
export function gitStatusReads(read: (repoId: string) => GitWorkspaceStatus | Promise<GitWorkspaceStatus>) {
  return {
    getGitStatus: async (_projectId: string, repoId: string) => gitStatusSummary(await read(repoId)),
    listGitChanges: async (_projectId: string, repoId: string) => gitChangesPage(await read(repoId)),
  };
}
