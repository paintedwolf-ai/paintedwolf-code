import type { LycaonClient } from "../../api/client.ts";
import type {
  GitBranchEntry,
  GitMutationResult,
  GitWorktreeLandResult,
  GitWorktreeView,
} from "../../api/types.ts";
import { loadGitWorkspaceStatus, type GitWorkspaceStatus } from "./git-workspace-status.ts";
import {
  beginGitProjection,
  refreshGitRepos,
  refreshGitStatus,
  unavailableStatus,
  type GitProjection,
} from "./git-status-reads.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { invalidateWorkspace } from "../../files/source/workspace-invalidation.ts";

export type WorktreeLandOutcome = GitWorktreeLandResult & {
  current?: string;
};

function errorDetails(err: unknown): Record<string, unknown> {
  if (typeof err !== "object" || err === null) return {};
  const details = (err as { details?: unknown }).details;
  if (typeof details !== "object" || details === null) return {};
  return details as Record<string, unknown>;
}

function isWorktreeLandBlocked(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  return (err as { code?: unknown }).code === "worktree_land_blocked";
}

function landBlockedOutcome(err: unknown): WorktreeLandOutcome {
  const d = errorDetails(err);
  const reason = typeof d.reason === "string" ? d.reason : "";
  const base =
    typeof d.base_branch === "string" ? d.base_branch : "";
  const conflicts = Array.isArray(d.conflicts)
    ? d.conflicts.filter((p): p is string => typeof p === "string")
    : [];
  const current = typeof d.current === "string" ? d.current : undefined;
  return {
    landed: false,
    base_branch: base,
    commits: 0,
    reason,
    conflicts,
    ...(current ? { current } : {}),
  };
}

/**
 * A write's projection displaced any read in flight, including the first repository-set load.
 * A failed write re-reads in its place without holding the error, or `busy`, behind that read.
 */
function rereadAfterFailedWrite(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string | undefined,
  projection: GitProjection,
): void {
  void refreshGitStatus(appStore, client, projectId, sessionId, { projection });
}

async function publishGitWrite(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string | undefined,
  projection: GitProjection,
  settle: () => Promise<GitWorkspaceStatus>,
): Promise<GitWorkspaceStatus> {
  let status: GitWorkspaceStatus;
  try {
    status = await settle();
  } catch (err) {
    rereadAfterFailedWrite(appStore, client, projectId, sessionId, projection);
    throw err;
  }
  if (projection.current()) appStore.actions.setGitStatus(status);
  await refreshGitRepos(appStore, client, projectId, sessionId, projection);
  return status;
}

function applyGitWrite(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId: string | undefined,
  projection: GitProjection,
  write: Promise<GitMutationResult>,
): Promise<GitWorkspaceStatus> {
  return publishGitWrite(appStore, client, projectId, sessionId, projection, async () => {
    const result = await write;
    invalidateWorkspace(client, projectId);
    return loadGitWorkspaceStatus(client, projectId, result.repo_id || repoId, sessionId);
  });
}

export async function listBranches(
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitBranchEntry[]> {
  if (!projectId.trim() || !repoId.trim()) return [];
  const view = await client.listGitBranches(projectId, repoId, sessionId);
  return view.branches;
}

export async function checkoutBranch(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  branch: string,
  create: boolean,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, projection,
    client.checkoutGit(projectId, repoId, { branch, create }, sessionId));
}

export async function discardAll(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, projection,
    client.discardGit(projectId, repoId, sessionId));
}

export async function pushChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, projection,
    client.pushGit(projectId, repoId, sessionId));
}

export async function pullChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, projection,
    client.pullGit(projectId, repoId, sessionId));
}

export async function initRepo(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  rootId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return publishGitWrite(appStore, client, projectId, sessionId, projection, async () => {
    const repo = await client.createGitRepo(projectId, { root_id: rootId });
    invalidateWorkspace(client, projectId);
    return repo.available && repo.repo_id
      ? loadGitWorkspaceStatus(client, projectId, repo.repo_id, sessionId)
      : unavailableStatus(repo.root_ids);
  });
}

export async function commitChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  message: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, projection,
    client.commitGit(projectId, repoId, { message }, sessionId));
}

export async function stashChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const projection = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, projection,
    client.stashGit(projectId, repoId, {}, sessionId));
}

export async function draftCommitMessage(
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<string> {
  const resp = await client.draftGitCommitMessage(projectId, repoId, sessionId);
  return resp.message;
}

export async function refreshWorktree(
  client: LycaonClient,
  projectId: string,
  sessionId: string,
): Promise<GitWorktreeView | undefined> {
  if (!projectId.trim() || !sessionId.trim()) return undefined;
  return client.getGitWorktree(sessionId);
}

export async function bindWorktree(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string,
  repoId: string,
  branch?: string,
): Promise<GitWorktreeView> {
  const projection = beginGitProjection(appStore);
  let view: GitWorktreeView;
  try {
    view = await client.bindGitWorktree(sessionId, {
      repo_id: repoId,
      branch: branch?.trim() || `session/${sessionId}`,
    });
  } catch (err) {
    rereadAfterFailedWrite(appStore, client, projectId, sessionId, projection);
    throw err;
  } finally {
    // A failed create can retain a recoverable binding on the host.
    invalidateWorkspace(client, projectId, sessionId);
  }
  await refreshGitStatus(appStore, client, projectId, sessionId, { projection });
  return view;
}

export async function landWorktree(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string,
): Promise<WorktreeLandOutcome> {
  const projection = beginGitProjection(appStore);
  try {
    return await client.landGitWorktree(sessionId);
  } catch (err) {
    if (isWorktreeLandBlocked(err)) {
      return landBlockedOutcome(err);
    }
    throw err;
  } finally {
    invalidateWorkspace(client, projectId);
    await refreshGitStatus(appStore, client, projectId, sessionId, { projection }).catch(
      () => undefined,
    );
  }
}

export async function unbindWorktree(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string,
): Promise<void> {
  const projection = beginGitProjection(appStore);
  try {
    await client.deleteGitWorktree(sessionId);
  } catch (err) {
    rereadAfterFailedWrite(appStore, client, projectId, sessionId, projection);
    throw err;
  } finally {
    invalidateWorkspace(client, projectId, sessionId);
  }
  await refreshGitStatus(appStore, client, projectId, sessionId, { projection });
}
