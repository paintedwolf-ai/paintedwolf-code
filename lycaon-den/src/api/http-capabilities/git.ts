import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
import type { OperationQuery } from "../operations.generated.ts";
import type {
  GitChangesPage,
  GitMutationResult,
  GitStatusSummary,
  GitReposView,
  GitRepoEntry,
  GitBranchesView,
  GitCheckoutRequest,
  GitInitRequest,
  GitCommitRequest,
  GitStashRequest,
  GitCommitMessageResponse,
  GitWorktreeView,
  GitWorktreeLandResult,
  GitWorktreeBindRequest,
} from "../types.ts";

export interface GitClient {
  listGitRepos(projectId: string, sessionId?: string): Promise<GitReposView>;
  getGitStatus(projectId: string, repoId: string, sessionId?: string): Promise<GitStatusSummary>;
  listGitChanges(projectId: string, repoId: string, query?: OperationQuery<"listGitChanges">): Promise<GitChangesPage>;
  listGitBranches(projectId: string, repoId: string, sessionId?: string): Promise<GitBranchesView>;
  checkoutGit(projectId: string, repoId: string, req: GitCheckoutRequest, sessionId?: string): Promise<GitMutationResult>;
  createGitRepo(projectId: string, req: GitInitRequest): Promise<GitRepoEntry>;
  discardGit(projectId: string, repoId: string, sessionId?: string): Promise<GitMutationResult>;
  pushGit(projectId: string, repoId: string, sessionId?: string): Promise<GitMutationResult>;
  pullGit(projectId: string, repoId: string, sessionId?: string): Promise<GitMutationResult>;
  commitGit(projectId: string, repoId: string, req: GitCommitRequest, sessionId?: string): Promise<GitMutationResult>;
  stashGit(projectId: string, repoId: string, req: GitStashRequest, sessionId?: string): Promise<GitMutationResult>;
  draftGitCommitMessage(projectId: string, repoId: string, sessionId?: string): Promise<GitCommitMessageResponse>;
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
    getGitStatus: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/status${formatQuery({ session_id: sessionId })}`),
    listGitChanges: (projectId, repoId, query = {}) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/changes${formatQuery({ ...query })}`),
    listGitBranches: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/branches${formatQuery({ session_id: sessionId })}`),
    checkoutGit: (projectId, repoId, req, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/checkout${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    createGitRepo: (projectId, req) => j(`/v1/projects/${id(projectId)}/git/repos`, jsonRequest("POST", req)),
    discardGit: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/discard${formatQuery({ session_id: sessionId })}`, { method: "POST" }),
    pushGit: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/push${formatQuery({ session_id: sessionId })}`, { method: "POST" }),
    pullGit: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/pull${formatQuery({ session_id: sessionId })}`, { method: "POST" }),
    commitGit: (projectId, repoId, req, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/commit${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    stashGit: (projectId, repoId, req, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/stash${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    draftGitCommitMessage: (projectId, repoId, sessionId) =>
      j(`/v1/projects/${id(projectId)}/git/repos/${id(repoId)}/commit-message${formatQuery({ session_id: sessionId })}`, { method: "POST" }),
    getGitWorktree: (sessionId) => j(`/v1/sessions/${id(sessionId)}/git-worktree`),
    bindGitWorktree: (sessionId, req) => j(`/v1/sessions/${id(sessionId)}/git-worktree`, jsonRequest("PUT", req)),
    landGitWorktree: (sessionId) => j(`/v1/sessions/${id(sessionId)}/git-worktree/land`, { method: "POST" }),
    deleteGitWorktree: (sessionId) => j(`/v1/sessions/${id(sessionId)}/git-worktree`, { method: "DELETE" }),
  };
}
