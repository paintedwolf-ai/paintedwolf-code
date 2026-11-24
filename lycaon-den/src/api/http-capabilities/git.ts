import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
import type { OperationQuery } from "../operations.generated.ts";
import type {
  GitChangesPage,
  GitDiffView,
  GitMutationResult,
  GitStatusSummary,
  GitReposView,
  GitRepoEntry,
  GitBranchesView,
  GitCheckoutRequest,
  GitInitRequest,
  GitCommitRequest,
  GitStashRequest,
  GitStashesView,
  GitCommitMessageResponse,
  GitWorktreeView,
  GitWorktreesView,
  GitWorktreeLandResult,
  GitWorktreeBindRequest,
} from "../types.ts";

export type GitDiscardGuard = {
  expectedHead?: string;
  expectedRevision?: number;
};

export type GitRemoteRef = {
  remote?: string;
  ref?: string;
};

export interface GitClient {
  listGitRepos(projectId: string, sessionId?: string): Promise<GitReposView>;
  listGitWorktrees(projectId: string, repoId: string, sessionId?: string): Promise<GitWorktreesView>;
  getGitDiff(projectId: string, repoId: string, query?: OperationQuery<"getGitDiff">): Promise<GitDiffView>;
  listGitStashes(projectId: string, repoId: string, query?: OperationQuery<"listGitStashes">): Promise<GitStashesView>;
  getGitStatus(projectId: string, repoId: string, sessionId?: string, worktreeId?: string): Promise<GitStatusSummary>;
  listGitChanges(projectId: string, repoId: string, query?: OperationQuery<"listGitChanges">): Promise<GitChangesPage>;
  listGitBranches(projectId: string, repoId: string, sessionId?: string, worktreeId?: string): Promise<GitBranchesView>;
  checkoutGit(projectId: string, repoId: string, req: GitCheckoutRequest, sessionId?: string, worktreeId?: string): Promise<GitMutationResult>;
  createGitRepo(projectId: string, req: GitInitRequest): Promise<GitRepoEntry>;
  discardGit(projectId: string, repoId: string, sessionId?: string, worktreeId?: string, guard?: GitDiscardGuard): Promise<GitMutationResult>;
  pushGit(projectId: string, repoId: string, sessionId?: string, worktreeId?: string, remoteRef?: GitRemoteRef): Promise<GitMutationResult>;
  pullGit(projectId: string, repoId: string, sessionId?: string, worktreeId?: string, remoteRef?: GitRemoteRef): Promise<GitMutationResult>;
  commitGit(projectId: string, repoId: string, req: GitCommitRequest, sessionId?: string, worktreeId?: string): Promise<GitMutationResult>;
  stashGit(projectId: string, repoId: string, req: GitStashRequest, sessionId?: string, worktreeId?: string): Promise<GitMutationResult>;
  draftGitCommitMessage(projectId: string, repoId: string, sessionId?: string, worktreeId?: string): Promise<GitCommitMessageResponse>;
  getGitWorktree(sessionId: string): Promise<GitWorktreeView>;
  bindGitWorktree(sessionId: string, req: GitWorktreeBindRequest): Promise<GitWorktreeView>;
  landGitWorktree(sessionId: string): Promise<GitWorktreeLandResult>;
  deleteGitWorktree(sessionId: string): Promise<void>;
}

export function createGitClient(j: JsonRequester): GitClient {
  const id = encodeURIComponent;
  return {
    listGitRepos: (projectId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos${formatQuery({ session_id: sessionId })}`),
    listGitWorktrees: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/worktrees${formatQuery({ session_id: sessionId })}`),
    getGitDiff: (projectId, repoId, query = {}) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/diff${formatQuery({ ...query })}`),
    listGitStashes: (projectId, repoId, query = {}) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/stashes${formatQuery({ ...query })}`),
    getGitStatus: (projectId, repoId, sessionId, worktreeId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/status${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`),
    listGitChanges: (projectId, repoId, query = {}) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/changes${formatQuery({ ...query })}`),
    listGitBranches: (projectId, repoId, sessionId, worktreeId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/branches${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`),
    checkoutGit: (projectId, repoId, req, sessionId, worktreeId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/checkout${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, jsonRequest("POST", req)),
    createGitRepo: (projectId, req) => j(`/v1/projects/${id(projectId)}/git/repos`, jsonRequest("POST", req)),
    discardGit: (projectId, repoId, sessionId, worktreeId, guard) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/discard${formatQuery({
        session_id: sessionId,
        worktree_id: worktreeId,
        expected_head: guard?.expectedHead,
        expected_revision: guard?.expectedRevision,
      })}`, { method: "POST" }),
    pushGit: (projectId, repoId, sessionId, worktreeId, remoteRef) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/push${formatQuery({
        session_id: sessionId,
        worktree_id: worktreeId,
        remote: remoteRef?.remote,
        ref: remoteRef?.ref,
      })}`, { method: "POST" }),
    pullGit: (projectId, repoId, sessionId, worktreeId, remoteRef) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/pull${formatQuery({
        session_id: sessionId,
        worktree_id: worktreeId,
        remote: remoteRef?.remote,
        ref: remoteRef?.ref,
      })}`, { method: "POST" }),
    commitGit: (projectId, repoId, req, sessionId, worktreeId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/commit${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, jsonRequest("POST", req)),
    stashGit: (projectId, repoId, req, sessionId, worktreeId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/stash${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, jsonRequest("POST", req)),
    draftGitCommitMessage: (projectId, repoId, sessionId, worktreeId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/commit-message${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, { method: "POST" }),
    getGitWorktree: (sessionId) => j(`/v1/sessions/${id(sessionId)}/git-worktree`),
    bindGitWorktree: (sessionId, req) => j(`/v1/sessions/${id(sessionId)}/git-worktree`, jsonRequest("PUT", req)),
    landGitWorktree: (sessionId) => j(`/v1/sessions/${id(sessionId)}/git-worktree/land`, { method: "POST" }),
    deleteGitWorktree: (sessionId) => j(`/v1/sessions/${id(sessionId)}/git-worktree`, { method: "DELETE" }),
  };
}
