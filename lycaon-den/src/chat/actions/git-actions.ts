import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  GitBranchEntry,
  GitMutationResult,
  GitWorktreeLandResult,
  GitWorktreeView,
} from "../../api/types.ts";
import { loadGitWorkspaceStatus, type GitWorkspaceStatus } from "./git-workspace-status.ts";
import { resolveScope } from "../../components/git-repo-scope.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { valueOf } from "../../store/load-state.ts";
import { invalidateWorkspace } from "../../files/source/workspace-invalidation.ts";

export type WorktreeLandOutcome = GitWorktreeLandResult & {
  current?: string;
};

type GitStatusFlight = {
  client: LycaonClient;
  promise: Promise<GitWorkspaceStatus | undefined>;
  isCurrent: () => boolean;
};

const gitStatusFlights = new WeakMap<AppStore, Map<string, GitStatusFlight>>();
const gitProjections = new WeakMap<AppStore, object>();
const [flightsRevision, setFlightsRevision] = createSignal(0);

/** True while a status read for this store is in flight; every flight ends. */
export function gitStatusRefreshPending(appStore: AppStore): boolean {
  flightsRevision();
  return (gitStatusFlights.get(appStore)?.size ?? 0) > 0;
}

function beginGitProjection(appStore: AppStore): () => boolean {
  const epoch = appStore.state.sessionViewEpoch;
  const pin = appStore.state.gitScopePin;
  const boardEpoch = appStore.state.boardEventEpoch;
  const request = {};
  gitProjections.set(appStore, request);
  return () => appStore.state.sessionViewEpoch === epoch &&
    appStore.state.boardEventEpoch === boardEpoch &&
    appStore.state.gitScopePin === pin && gitProjections.get(appStore) === request;
}


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

function unavailableStatus(rootIds: string[] = []): GitWorkspaceStatus {
  return {
    available: false,
    repo_id: "",
    root_ids: rootIds,
    ahead: 0,
    behind: 0,
    dirty: false,
    staged_count: 0,
    unstaged_count: 0,
    changed_count: 0,
    files: [],
  };
}

export async function refreshGitRepos(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId?: string,
  shouldApply: () => boolean = beginGitProjection(appStore),
): Promise<{ repos: import("../../api/types.ts").GitRepoEntry[]; active_repo_id: string }> {
  if (!projectId.trim()) {
    return { repos: [], active_repo_id: "" };
  }
  const view = await client.listGitRepos(projectId, sessionId);
  const active = view.active_repo_id ?? "";
  if (shouldApply()) appStore.actions.setGitRepos(view.repos, active);
  return { repos: view.repos, active_repo_id: active };
}

export async function refreshGitStatus(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId?: string,
  options?: { repoId?: string; refreshSet?: boolean; shouldApply?: () => boolean },
): Promise<GitWorkspaceStatus | undefined> {
  const key = [
    projectId.trim(),
    sessionId?.trim() ?? "",
    options?.repoId?.trim() ?? "",
    options?.refreshSet === false ? "status" : "repos+status",
  ].join("\u0000");
  let flights = gitStatusFlights.get(appStore);
  if (!flights) {
    flights = new Map();
    gitStatusFlights.set(appStore, flights);
  }
  const existing = flights.get(key);
  if (existing?.client === client && existing.isCurrent()) return existing.promise;
  const shouldApply = options?.shouldApply ?? beginGitProjection(appStore);
  const flight: Promise<GitWorkspaceStatus | undefined> = Promise.resolve().then(() =>
    loadGitStatus(
      appStore,
      client,
      projectId,
      sessionId,
      options,
      isCurrent,
    ),
  );
  const isCurrent = () => shouldApply() && flights?.get(key)?.promise === flight;
  flights.set(key, { client, promise: flight, isCurrent });
  setFlightsRevision((n) => n + 1);
  try {
    return await flight;
  } finally {
    if (flights.get(key)?.promise === flight) {
      flights.delete(key);
      setFlightsRevision((n) => n + 1);
    }
  }
}

async function loadGitStatus(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string | undefined,
  options: { repoId?: string; refreshSet?: boolean; shouldApply?: () => boolean } | undefined,
  isCurrent: () => boolean,
): Promise<GitWorkspaceStatus | undefined> {
  if (!projectId.trim()) return undefined;
  try {
    return await loadGitStatusRemote(appStore, client, projectId, sessionId, options, isCurrent);
  } catch (err) {
    // Preserve read failures for the checkout error state.
    if (isCurrent()) appStore.actions.setGitStatusLoadFailed(err);
    return undefined;
  }
}

async function loadGitStatusRemote(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string | undefined,
  options: { repoId?: string; refreshSet?: boolean; shouldApply?: () => boolean } | undefined,
  isCurrent: () => boolean,
): Promise<GitWorkspaceStatus | undefined> {
  let repos = appStore.state.gitRepos ?? [];
  let active = appStore.state.gitActiveRepoId ?? "";
  if (options?.refreshSet !== false) {
    const view = await client.listGitRepos(projectId, sessionId);
    // Mutation follow-up reads complete after navigation, without repainting it.
    if (!isCurrent() && !options?.shouldApply) return undefined;
    if (isCurrent()) appStore.actions.setGitRepos(view.repos, view.active_repo_id ?? "");
    repos = view.repos;
    active = view.active_repo_id ?? "";
  }
  const pin = appStore.state.gitScopePin ?? null;
  const scope = resolveScope(repos, active, pin);
  let repoId = (options?.repoId ?? "").trim();
  if (!repoId && scope.kind === "repo") {
    repoId = scope.repoId;
  }
  if (!repoId) {
    if (scope.kind === "root") {
      const status = unavailableStatus([scope.rootId]);
      if (!isCurrent()) return undefined;
      appStore.actions.setGitStatus(status);
      return status;
    }
    return valueOf(appStore.state.gitStatus);
  }
  const status = await loadGitWorkspaceStatus(client, projectId, repoId, sessionId);
  if (!isCurrent()) return undefined;
  appStore.actions.setGitStatus(status);
  return status;
}

async function applyGitWrite(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId: string | undefined,
  shouldApply: () => boolean,
  write: Promise<GitMutationResult>,
): Promise<GitWorkspaceStatus> {
  const result = await write;
  invalidateWorkspace(client, projectId);
  const status = await loadGitWorkspaceStatus(client, projectId, result.repo_id || repoId, sessionId);
  if (shouldApply()) appStore.actions.setGitStatus(status);
  await refreshGitRepos(appStore, client, projectId, sessionId, shouldApply);
  return status;
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
  const shouldApply = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, shouldApply,
    client.checkoutGit(projectId, repoId, { branch, create }, sessionId));
}

export async function discardAll(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const shouldApply = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, shouldApply,
    client.discardGit(projectId, repoId, sessionId));
}

export async function pushChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const shouldApply = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, shouldApply,
    client.pushGit(projectId, repoId, sessionId));
}

export async function pullChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const shouldApply = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, shouldApply,
    client.pullGit(projectId, repoId, sessionId));
}

export async function initRepo(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  rootId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const shouldApply = beginGitProjection(appStore);
  const repo = await client.createGitRepo(projectId, { root_id: rootId });
  invalidateWorkspace(client, projectId);
  const status = repo.available && repo.repo_id
    ? await loadGitWorkspaceStatus(client, projectId, repo.repo_id, sessionId)
    : unavailableStatus(repo.root_ids);
  if (shouldApply()) appStore.actions.setGitStatus(status);
  await refreshGitRepos(appStore, client, projectId, sessionId, shouldApply);
  return status;
}

export async function commitChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  message: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const shouldApply = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, shouldApply,
    client.commitGit(projectId, repoId, { message }, sessionId));
}

export async function stashChanges(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  const shouldApply = beginGitProjection(appStore);
  return applyGitWrite(appStore, client, projectId, repoId, sessionId, shouldApply,
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
  const shouldApply = beginGitProjection(appStore);
  let view: GitWorktreeView;
  try {
    view = await client.bindGitWorktree(sessionId, {
      repo_id: repoId,
      branch: branch?.trim() || `session/${sessionId}`,
    });
  } finally {
    // A failed create can retain a recoverable binding on the host.
    invalidateWorkspace(client, projectId, sessionId);
  }
  await refreshGitStatus(appStore, client, projectId, sessionId, { shouldApply });
  return view;
}

export async function landWorktree(
  appStore: AppStore,
  client: LycaonClient,
  projectId: string,
  sessionId: string,
): Promise<WorktreeLandOutcome> {
  const shouldApply = beginGitProjection(appStore);
  try {
    return await client.landGitWorktree(sessionId);
  } catch (err) {
    if (isWorktreeLandBlocked(err)) {
      return landBlockedOutcome(err);
    }
    throw err;
  } finally {
    invalidateWorkspace(client, projectId);
    await refreshGitStatus(appStore, client, projectId, sessionId, { shouldApply }).catch(
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
  const shouldApply = beginGitProjection(appStore);
  try {
    await client.deleteGitWorktree(sessionId);
  } finally {
    invalidateWorkspace(client, projectId, sessionId);
  }
  await refreshGitStatus(appStore, client, projectId, sessionId, { shouldApply });
}
